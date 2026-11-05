package protocol

import (
	"bytes"
	"embed"
	"errors"
	"fmt"
	"path"
	"strings"
	"sync"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/santhosh-tekuri/jsonschema/v6/kind"
)

//go:embed schemas/*.json
var schemaFS embed.FS

// SchemaBase 与 job-docs schemas 的 $id 前缀一致。
const SchemaBase = "https://jobcenter.local/schemas/ws/1.0/"

// SchemaFS 暴露内嵌 schema（契约测试使用）。
func SchemaFS() embed.FS { return schemaFS }

// Validator 按消息类型校验整条消息（信封 + data）。
type Validator struct {
	byType   map[string]*jsonschema.Schema
	envelope *jsonschema.Schema
}

var (
	defaultValidator *Validator
	defaultErr       error
	once             sync.Once
)

// DefaultValidator 编译一次内嵌 schema。
func DefaultValidator() (*Validator, error) {
	once.Do(func() { defaultValidator, defaultErr = NewValidator() })
	return defaultValidator, defaultErr
}

func NewValidator() (*Validator, error) {
	c := jsonschema.NewCompiler()
	c.AssertFormat()
	ents, err := schemaFS.ReadDir("schemas")
	if err != nil {
		return nil, err
	}
	var names []string
	for _, e := range ents {
		if !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		b, _ := schemaFS.ReadFile(path.Join("schemas", e.Name()))
		doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(b))
		if err != nil {
			return nil, fmt.Errorf("%s: %w", e.Name(), err)
		}
		if err := c.AddResource(SchemaBase+e.Name(), doc); err != nil {
			return nil, err
		}
		names = append(names, e.Name())
	}
	v := &Validator{byType: map[string]*jsonschema.Schema{}}
	for _, n := range names {
		s, err := c.Compile(SchemaBase + n)
		if err != nil {
			return nil, fmt.Errorf("compile %s: %w", n, err)
		}
		t := strings.TrimSuffix(n, ".json")
		if t == "envelope" {
			v.envelope = s
		} else {
			v.byType[t] = s
		}
	}
	if v.envelope == nil {
		return nil, errors.New("envelope.json missing")
	}
	return v, nil
}

// Types 返回已编译的消息类型数。
func (v *Validator) Types() int { return len(v.byType) }

// ValidateEnvelope 只校验信封（用于未知类型）。
func (v *Validator) ValidateEnvelope(raw []byte) error {
	inst, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		return err
	}
	return v.envelope.Validate(inst)
}

// Validate 校验一条完整消息。strict=false 时忽略 additionalProperties 违规
// （中心对 SDK 宽松：次版本新增字段时向前兼容；CI 契约测试用 strict=true）。
func (v *Validator) Validate(typ string, raw []byte, strict bool) error {
	s, ok := v.byType[typ]
	if !ok {
		return fmt.Errorf("unknown message type %q", typ)
	}
	inst, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		return err
	}
	err = s.Validate(inst)
	if err == nil || strict {
		return err
	}
	var ve *jsonschema.ValidationError
	if errors.As(err, &ve) && onlyAdditionalProps(ve) {
		return nil
	}
	return err
}

func onlyAdditionalProps(e *jsonschema.ValidationError) bool {
	if len(e.Causes) == 0 {
		_, ok := e.ErrorKind.(*kind.AdditionalProperties)
		return ok
	}
	for _, c := range e.Causes {
		if !onlyAdditionalProps(c) {
			return false
		}
	}
	return true
}
