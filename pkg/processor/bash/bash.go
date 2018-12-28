package bash

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/geliar/manopus/pkg/log"

	"github.com/geliar/manopus/pkg/payload"
	"github.com/geliar/manopus/pkg/processor"
)

func init() {
	ctx := log.Logger.WithContext(context.Background())
	l := logger(ctx)
	l.Debug().Msg("Registering processor in the catalog")
	processor.Register(ctx, new(Bash))
}

type Bash struct {
}

func (p *Bash) Type() string {
	return serviceName
}

func (p *Bash) Run(ctx context.Context, config *processor.ProcessorConfig, payload *payload.Payload) (result interface{}, err error) {
	l := logger(ctx)
	script := p.collectScript(ctx, config.Script)
	if script == "" {
		return nil, processor.ErrParseScript
	}
	cmd := exec.Command("/bin/bash", "/dev/stdin")
	cmd.Env = os.Environ()
	stdin, err := cmd.StdinPipe()
	if err != nil {
		l.Error().Err(err).Msg("Cannot open stdin of executing process")
		return nil, err
	}
	pp := preparePayload("ENV_", payload.Env)
	pp += preparePayload("MATCH_", payload.Match)
	pp += preparePayload("EXPORT_", payload.Export)
	pp += preparePayload("REQ_", payload.Req)
	println(pp)
	println(script)
	go func() {
		_, _ = stdin.Write([]byte(pp))
		_, _ = stdin.Write([]byte(script))
		_ = stdin.Close()
	}()
	buf, err := cmd.Output()
	if err != nil {
		l.Debug().Err(err).Msg("Error when executing script")
	}
	return string(buf), err
}

func (Bash) collectScript(ctx context.Context, script interface{}) (result string) {
	l := logger(ctx)
	switch v := script.(type) {
	case []interface{}:
		var builder strings.Builder
		for i := range v {
			switch s := v[i].(type) {
			case string, int, float64:
				builder.WriteString(fmt.Sprint(s))
				builder.WriteString("\n")
			default:
				l.Error().Msgf("Cannot parse script in line %d, skipping", i)
				return
			}
		}
		return builder.String()
	case string:
		return v + "\n"
	}
	return ""
}

func preparePayload(prefix string, payload map[string]interface{}) (result string) {
	var builder strings.Builder
	for k, p := range payload {
		key := prefix + k
		switch v := p.(type) {
		case string, int, float64:
			builder.WriteString(prepareKey(key))
			builder.WriteString(`="`)
			builder.WriteString(prepareValue(fmt.Sprint(v)))
			builder.WriteString("\"\n")
		case []interface{}:
			for i := range v {
				switch a := v[i].(type) {
				case string, int, float64:
					builder.WriteString(prepareKey(key))
					builder.WriteString(fmt.Sprintf(`[%d]="`, i))
					builder.WriteString(prepareValue(fmt.Sprint(a)))
					builder.WriteString("\"\n")
				}
			}
		case map[string]interface{}:
			builder.WriteString(preparePayload(key+"_", v))
		}
	}
	return builder.String()
}

func prepareKey(str string) string {
	str = strings.Replace(str, " ", "_", -1)
	str = strings.Replace(str, "-", "_", -1)
	str = strings.ToUpper(str)
	return str
}

func prepareValue(str string) string {
	str = strings.Replace(str, `"`, `\"`, -1)
	return str
}
