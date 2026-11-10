package jobmeta

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/robfig/cron/v3"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

var (
	JobCodeRe     = regexp.MustCompile(`^[a-z0-9][a-z0-9._:-]{0,127}$`)
	versionRe     = regexp.MustCompile(`^([0-9]+)(?:\.([0-9]+))?(?:\.([0-9]+))?(?:([-+])([0-9A-Za-z.-]+))?$`)
	ErrInvalidVer = errors.New("handler_version must be MAJOR[.MINOR[.PATCH]][-pre|+build]")
	ErrInvalidCron = errors.New("cron must have exactly 6 fields: second minute hour day-of-month month day-of-week")
	cronParser     = cron.NewParser(cron.Second | cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow | cron.Descriptor)
)

type Version struct {
	Major, Minor, Patch int
	Pre                 string
	Raw                 string
}

func ParseVersion(s string) (Version, error) {
	m := versionRe.FindStringSubmatch(s)
	if m == nil || len(s) > 32 {
		return Version{}, ErrInvalidVer
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
		return Version{}, ErrInvalidVer
	}
	if v.Minor, err = atoi(m[2]); err != nil {
		return Version{}, ErrInvalidVer
	}
	if v.Patch, err = atoi(m[3]); err != nil {
		return Version{}, ErrInvalidVer
	}
	if m[4] == "-" {
		v.Pre = m[5]
	}
	return v, nil
}

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

const MaxSchemaBytes = 64 << 10
const EmptySchemaJSON = "{}"

func NormalizeSchema(raw json.RawMessage) (canonical []byte, hash string, err error) {
	if len(bytes.TrimSpace(raw)) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		raw = json.RawMessage(EmptySchemaJSON)
	}
	if len(raw) > MaxSchemaBytes {
		return nil, "", fmt.Errorf("params_schema exceeds %d bytes", MaxSchemaBytes)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var doc any
	if err := dec.Decode(&doc); err != nil {
		return nil, "", fmt.Errorf("params_schema: %w", err)
	}
	if _, ok := doc.(map[string]any); !ok {
		return nil, "", errors.New("params_schema must be a JSON object")
	}
	canonical, err = json.Marshal(doc)
	if err != nil {
		return nil, "", err
	}
	if err := compileSchema(canonical); err != nil {
		return nil, "", err
	}
	sum := sha256.Sum256(canonical)
	return canonical, hex.EncodeToString(sum[:]), nil
}

type denyLoader struct{}

func (denyLoader) Load(url string) (any, error) {
	return nil, fmt.Errorf("remote $ref not allowed: %s", url)
}

func compileSchema(raw []byte) error {
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		return err
	}
	c := jsonschema.NewCompiler()
	c.UseLoader(denyLoader{})
	c.AssertFormat()
	const loc = "mem://params_schema.json"
	if err := c.AddResource(loc, doc); err != nil {
		return err
	}
	if _, err := c.Compile(loc); err != nil {
		return fmt.Errorf("params_schema does not compile: %w", err)
	}
	return nil
}

func ValidateCron(expr string) error {
	e := strings.TrimSpace(expr)
	if e == "" || strings.HasPrefix(e, "CRON_TZ=") || strings.HasPrefix(e, "TZ=") || strings.HasPrefix(e, "@") {
		return ErrInvalidCron
	}
	if len(strings.Fields(e)) != 6 {
		return ErrInvalidCron
	}
	if _, err := cronParser.Parse(e); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidCron, err)
	}
	return nil
}

// JobAllowed nil=全部允许；[]=全禁；支持 prefix.* / *。
func JobAllowed(allowed []string, code string) bool {
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
