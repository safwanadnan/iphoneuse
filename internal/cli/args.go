package cli

import (
	"fmt"
	"strconv"
	"strings"
)

// args is a tiny parser that lets flags appear anywhere, adb-style:
// `iadb -s phone tap 10 20` and `iadb tap 10 20 -s phone` both work.
type args struct {
	pos   []string
	flags map[string]string
}

// valueFlags take a value; every other flag is a boolean switch.
var valueFlags = map[string]bool{
	"s": true, "serial": true, "device": true, "o": true, "output": true, "team": true,
	"app": true, "duration": true, "quality": true, "scale": true, "limit": true,
	"timeout": true, "text": true, "ref": true, "n": true, "lines": true,
}

var aliases = map[string]string{"serial": "s", "device": "s", "output": "o", "lines": "n"}

func parseArgs(in []string) (args, error) {
	a := args{flags: map[string]string{}}
	for i := 0; i < len(in); i++ {
		arg := in[i]
		if arg == "--" {
			a.pos = append(a.pos, in[i+1:]...)
			break
		}
		if !strings.HasPrefix(arg, "-") || arg == "-" || isNumber(arg) {
			a.pos = append(a.pos, arg)
			continue
		}
		name := strings.TrimLeft(arg, "-")
		value := ""
		hasValue := false
		if eq := strings.IndexByte(name, '='); eq >= 0 {
			name, value, hasValue = name[:eq], name[eq+1:], true
		}
		if alias, ok := aliases[name]; ok {
			name = alias
		}
		if valueFlags[name] {
			if !hasValue {
				if i+1 >= len(in) {
					return a, fmt.Errorf("flag -%s needs a value", name)
				}
				i++
				value = in[i]
			}
			a.flags[name] = value
		} else {
			if !hasValue {
				value = "true"
			}
			a.flags[name] = value
		}
	}
	return a, nil
}

func isNumber(s string) bool {
	_, err := strconv.ParseFloat(s, 64)
	return err == nil
}

func (a args) str(name string) string { return a.flags[name] }

func (a args) bool(name string) bool {
	v, ok := a.flags[name]
	return ok && v != "false" && v != "0"
}

func (a args) float(name string, def float64) (float64, error) {
	v, ok := a.flags[name]
	if !ok {
		return def, nil
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil {
		return 0, fmt.Errorf("-%s must be a number", name)
	}
	return f, nil
}

func (a args) int(name string, def int) (int, error) {
	v, ok := a.flags[name]
	if !ok {
		return def, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, fmt.Errorf("-%s must be a whole number", name)
	}
	return n, nil
}
