package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"text/template"

	"github.com/sqlc-dev/plugin-sdk-go/codegen"
	"github.com/sqlc-dev/plugin-sdk-go/plugin"
)

func main() {
	codegen.Run(generate)
}

type Options struct {
	Template         string `json:"template" yaml:"template"`
	TemplateContent  string `json:"template_content" yaml:"template_content"`
	Filename         string `json:"filename" yaml:"filename"`
	FormatterCommand string `json:"formatter_cmd" yaml:"formatter_cmd"`
	Out              string `json:"out" yaml:"out"`
}

func parseOpts(req *plugin.GenerateRequest) (*Options, error) {
	var options Options
	if len(req.PluginOptions) == 0 {
		return &options, nil
	}
	if err := json.Unmarshal(req.PluginOptions, &options); err != nil {
		return nil, fmt.Errorf("unmarshalling plugin options: %w", err)
	}

	return &options, nil
}

func generate(ctx context.Context, req *plugin.GenerateRequest) (*plugin.GenerateResponse, error) {
	// fmt.Println(req)
	options, _ := parseOpts(req)
	templateFileName := options.Template

	pluginOptions := make(map[string]any)
	err := json.Unmarshal(req.PluginOptions, &pluginOptions)
	if err != nil {
		log.Fatal("failed to unmarshal plugin options: ", err)
	}

	var tmpl *template.Template
	tmplContext := map[string]any{}
	funcMap := template.FuncMap{
		"contains": strings.Contains,
		// https://stackoverflow.com/a/18276968/1149933
		"dict": func(values ...any) (map[string]any, error) {
			if len(values)%2 != 0 {
				return nil, errors.New("invalid dict call")
			}
			dict := make(map[string]any, len(values)/2)
			for i := 0; i < len(values); i += 2 {
				key, ok := values[i].(string)
				if !ok {
					return nil, errors.New("dict keys must be strings")
				}
				dict[key] = values[i+1]
			}
			return dict, nil
		},
		"set": func(dict map[string]any, key string, value any) map[string]any {
			dict[key] = value
			return dict
		},
		"get": func(dict map[string]any, key string) (any, error) {
			val, ok := dict[key]
			if !ok {
				return nil, fmt.Errorf("no value for key %s", key)
			}
			return val, nil
		},
		"empty": func(value any) bool {
			return value == "" || value == 0 || value == nil
		},
		"setContext": func(key string, value any) map[string]any {
			tmplContext[key] = value
			return tmplContext
		},
		"hasKey": func(dict map[string]any, key string) bool {
			_, ok := dict[key]
			return ok
		},
		"getContext": func(key string) any {
			return tmplContext[key]
		},
		"hasContextKey": func(key string) bool {
			_, ok := tmplContext[key]
			return ok
		},
		"getPluginOption": func(name string) any {
			option, ok := pluginOptions[name]
			if !ok {
				return ""
			}
			return option
		},
		"split":   strings.Split,
		"toLower": strings.ToLower,
		"toUpper": strings.ToUpper,
		"add": func(a int, b int) int {
			return a + b
		},
		"sub": func(a int, b int) int {
			return a - b
		},
		"toPascalCase": func(s string) string {
			buffer := strings.Builder{}
			for i := 0; i < len(s); i++ {
				char := s[i]
				if i == 0 {
					if 'a' <= char && char <= 'z' {
						char -= 'a' - 'A'
					}

				}
				if char != '_' {
					buffer.WriteByte(char)
					continue
				}
				i += 1
				if i >= len(s) {
					break
				}
				char = s[i]
				if 'a' <= char && char <= 'z' {
					char -= 'a' - 'A'
				}
				buffer.WriteByte(char)
			}
			return buffer.String()
		},
		"snakeToCamelCase": func(s string) string {
			buffer := strings.Builder{}
			for i := 0; i < len(s); i++ {
				char := s[i]
				if char != '_' {
					buffer.WriteByte(char)
					continue
				}
				i += 1
				if i >= len(s) {
					break
				}
				char = s[i]
				if 'a' <= char && char <= 'z' {
					char -= 'a' - 'A'
				}
				buffer.WriteByte(char)
			}
			return buffer.String()
		},
		"templateIntoString": func(name string, data any) string {
			buffer := strings.Builder{}
			tmpl.ExecuteTemplate(&buffer, name, data)
			return buffer.String()
		},
		"nindent": func(count int, value string) string {
			indentation := "\n" + strings.Repeat(" ", count)
			return indentation + strings.ReplaceAll(value, "\n", indentation)
		},
		"regexReplace": func(expr string, replacement string, src string) (string, error) {
			regex, err := regexp.Compile(expr)
			if err != nil {
				return "", err
			}
			return regex.ReplaceAllLiteralString(src, replacement), nil
		},
	}

	absPath, err := filepath.Abs(templateFileName)
	if err != nil {
		log.Fatalf("Failed to resolve absolute path for template: %v", err)
	}

	if options.TemplateContent != "" {
		tmpl, err = template.New("__content").Funcs(funcMap).Parse(options.TemplateContent)
	} else {
		tmpl, err = template.New(filepath.Base(absPath)).Funcs(funcMap).ParseFiles(absPath)
	}
	if err != nil {
		log.Fatalf("Error parsing template file: %v", err)
	}

	resp := plugin.GenerateResponse{}
	for i := range req.Queries {
		paramMap := make(map[string]int)
		for j := range req.Queries[i].Params {
			colName := req.Queries[i].Params[j].Column.Name
			val, ok := paramMap[colName]
			if !ok {
				paramMap[colName] = 1
				continue
			}
			paramMap[colName] = val + 1
			req.Queries[i].Params[j].Column.Name = colName + fmt.Sprintf("%v", val)
		}
	}
	var buf bytes.Buffer
	err = tmpl.Execute(&buf, req)
	if err != nil {
		log.Fatalf("Error executing template: %v", err)
	}

	if options.FormatterCommand != "" {
		execCommand := exec.Command("/usr/bin/env", "bash", "-c", options.FormatterCommand)
		execCommand.Stdin = bytes.NewReader(buf.Bytes())
		var output bytes.Buffer
		execCommand.Stdout = &output
		if err := execCommand.Run(); err != nil {
			log.Fatalf("Error executing formatter command: %v", err)
		}

		buf = output
	}

	resp.Files = append(resp.Files, &plugin.File{
		Name:     options.Filename,
		Contents: buf.Bytes(),
	})

	return &resp, nil
}
