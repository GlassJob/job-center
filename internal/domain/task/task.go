// Package task 定义任务能力（Task Definition）的领域规则：版本兼容、Cron、参数 Schema。
package task

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/robfig/cron/v3"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

var (
	TaskCodeRe    = regexp.MustCompile(`^[a-z0-9][a-z0-9._:-]{0,127}$`)
	ServiceCodeRe = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{1,63}$`)
	versionRe     = regexp.MustCompile(`^([0-9]+)(?:\.([0-9]+))?(?:\.([0-9]+))?(?:([-+])([0-9A-Za-z.-]+))?$`)

	ErrInvalidVersion = errors.New("handler_version must be MAJOR[.MINOR[.PATCH]][-pre|+build]")
	ErrInvalidCron    = errors.New("cron must have exactly 6 fields: second minute hour day-of-month month day-of-week")
)

// Version handler_version 解析结果。主版本相同即兼容（已确认 2026-11-05）。
type Version struct {
	Major, Minor, Patch int
	Pre                 string // "-xxx" 预发布；"+build" 元数据不参与比较
	Raw                 string
}

func ParseVersion(s string) (Version, error) {
	m := versionRe.FindStringSubmatch(s)
	if m == nil || len(s) > 32 {
		return Version{}, ErrInvalidVersion
	}
	atoi := func(x string) (int, error) {
		if x == "" {
			return 0, nil
		}
		return strconv.Atoi(x)
	}
	v := Version{Raw: s}
	var err error
	if v.Major, err = atoi(m[1]); err != nil {
		return Version{}, ErrInvalidVersion
	}
	if v.Minor, err = atoi(m[2]); err != nil {
		return Version{}, ErrInvalidVersion
	}
	if v.Patch, err = atoi(m[3]); err != nil {
		return Version{}, ErrInvalidVersion
	}
	if m[4] == "-" {
		v.Pre = m[5]
	}
	return v, nil
}

// Compare 返回 -1/0/1；预发布版本低于同号正式版。
func (a Version) Compare(b Version) int {
	for _, d := range [][2]int{{a.Major, b.Major}, {a.Minor, b.Minor}, {a.Patch, b.Patch}} {
		if d[0] != d[1] {
			if d[0] < d[1] {
				return -1
			}
			return 1
		}
	}
	switch {
	case a.Pre == b.Pre:
		return 0
	case a.Pre == "":
		return 1
	case b.Pre == "":
		return -1
	case a.Pre < b.Pre:
		return -1
	default:
		return 1
	}
}

// Compatible 主版本相同即兼容（1.0 与 1.2 兼容，2.0 不兼容）。
func (a Version) Compatible(b Version) bool { return a.Major == b.Major }

var cronParser = cron.NewParser(cron.Second | cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow | cron.Descriptor)

// ValidateCron 严格 6 字段；拒绝 CRON_TZ=/TZ= 前缀（时区统一走 job_schedule.timezone）。
func ValidateCron(expr string) (cron.Schedule, error) {
	e := strings.TrimSpace(expr)
	if e == "" || strings.HasPrefix(e, "CRON_TZ=") || strings.HasPrefix(e, "TZ=") || strings.HasPrefix(e, "@") {
		return nil, ErrInvalidCron
	}
	if len(strings.Fields(e)) != 6 {
		return nil, ErrInvalidCron
	}
	s, err := cronParser.Parse(e)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidCron, err)
	}
	return s, nil
}

// MaxSchemaBytes 参数 schema 上限。
const MaxSchemaBytes = 64 << 10

// EmptySchemaJSON 无 schema 时用于计算 hash 的规范值。
const EmptySchemaJSON = "{}"

// NormalizeSchema 校验并规范化 input_schema：必须是对象、≤64KiB、可编译、禁止远程 $ref。
// 返回规范 JSON（key 排序）与其 SHA-256。
func NormalizeSchema(raw json.RawMessage) (canonical []byte, hash string, err error) {
	if len(bytes.TrimSpace(raw)) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		raw = json.RawMessage(EmptySchemaJSON)
	}
	if len(raw) > MaxSchemaBytes {
		return nil, "", fmt.Errorf("input_schema exceeds %d bytes", MaxSchemaBytes)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var doc any
	if err := dec.Decode(&doc); err != nil {
		return nil, "", fmt.Errorf("input_schema: %w", err)
	}
	if _, ok := doc.(map[string]any); !ok {
		return nil, "", errors.New("input_schema must be a JSON object")
	}
	canonical, err = json.Marshal(doc) // encoding/json 对 map 按 key 排序 → 规范形式
	if err != nil {
		return nil, "", err
	}
	if _, err := CompileSchema(canonical); err != nil {
		return nil, "", err
	}
	sum := sha256.Sum256(canonical)
	return canonical, hex.EncodeToString(sum[:]), nil
}

type denyLoader struct{}

func (denyLoader) Load(url string) (any, error) {
	return nil, fmt.Errorf("remote $ref not allowed: %s", url)
}

// CompileSchema 编译参数 schema（禁止加载任何外部资源，防 SSRF/文件读取）。
func CompileSchema(raw []byte) (*jsonschema.Schema, error) {
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	c := jsonschema.NewCompiler()
	c.UseLoader(denyLoader{})
	c.AssertFormat()
	const loc = "mem://input_schema.json"
	if err := c.AddResource(loc, doc); err != nil {
		return nil, err
	}
	s, err := c.Compile(loc)
	if err != nil {
		return nil, fmt.Errorf("input_schema does not compile: %w", err)
	}
	return s, nil
}

// TaskAllowed 判断凭据的 allowed_task_codes 是否允许注册该 task_code；nil=全部允许，支持 "prefix.*"。
func TaskAllowed(allowed []string, code string) bool {
	if allowed == nil {
		return true
	}
	for _, a := range allowed {
		if a == code || a == "*" {
			return true
		}
		if p, ok := strings.CutSuffix(a, "*"); ok && strings.HasPrefix(code, p) {
			return true
		}
	}
	return false
}

// MaxStateBytes 任务持久化状态与默认参数对象大小上限（64 KiB）。
const MaxStateBytes = 64 << 10
const MaxParamsBytes = 64 << 10

// ValidateJSONObject 校验 raw 必须是顶层 JSON object 且编码后不超过 maxBytes。
func ValidateJSONObject(raw []byte, maxBytes int) (json.RawMessage, error) {
	if maxBytes <= 0 {
		maxBytes = MaxStateBytes
	}
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return nil, errors.New("must be a JSON object")
	}
	if len(trimmed) > maxBytes {
		return nil, fmt.Errorf("JSON object exceeds %d bytes", maxBytes)
	}
	dec := json.NewDecoder(bytes.NewReader(trimmed))
	dec.UseNumber()
	var doc any
	if err := dec.Decode(&doc); err != nil {
		return nil, fmt.Errorf("invalid JSON: %w", err)
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		return nil, errors.New("invalid JSON: trailing data")
	}
	if _, ok := doc.(map[string]any); !ok {
		return nil, errors.New("must be a JSON object")
	}
	canonical, err := json.Marshal(doc)
	if err != nil {
		return nil, err
	}
	if len(canonical) > maxBytes {
		return nil, fmt.Errorf("JSON object exceeds %d bytes", maxBytes)
	}
	return json.RawMessage(canonical), nil
}

// ValidateParamsAgainstSchema 用任务的 input_schema 校验参数对象；返回违反项明细（含 instance_path）。
func ValidateParamsAgainstSchema(schemaRaw, paramsRaw []byte) ([]map[string]string, error) {
	st := bytes.TrimSpace(schemaRaw)
	if len(st) == 0 || bytes.Equal(st, []byte("null")) || bytes.Equal(st, []byte("{}")) {
		return nil, nil
	}
	compiled, err := CompileSchema(st)
	if err != nil {
		return nil, err
	}
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(paramsRaw))
	if err != nil {
		return nil, err
	}
	err = compiled.Validate(doc)
	if err == nil {
		return nil, nil
	}
	var ve *jsonschema.ValidationError
	if errors.As(err, &ve) {
		bo := ve.BasicOutput()
		var out []map[string]string
		for _, u := range bo.Errors {
			if u.Error != nil {
				out = append(out, map[string]string{
					"instance_path": u.InstanceLocation,
					"message":       u.Error.String(),
				})
			}
		}
		if len(out) == 0 {
			out = []map[string]string{{"instance_path": "/" + strings.Join(ve.InstanceLocation, "/"), "message": ve.Error()}}
		}
		return out, ve
	}
	return nil, err
}
