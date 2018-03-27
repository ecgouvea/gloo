package swagger

import (
	"fmt"
	"sort"
	"strings"

	"github.com/go-openapi/spec"
	"github.com/pkg/errors"
	"github.com/solo-io/gloo-api/pkg/api/types/v1"
	"github.com/solo-io/gloo-function-discovery/internal/swagger"
	"github.com/solo-io/gloo-plugins/rest"
	"github.com/solo-io/gloo/pkg/log"
)

func GetFuncs(us *v1.Upstream) ([]*v1.Function, error) {
	swaggerSpec, err := getSwaggerSpecForUpsrteam(us)
	if err != nil {
		return nil, err
	}
	var consumesJson bool
	for _, contentType := range swaggerSpec.Consumes {
		if contentType == "application/json" {
			consumesJson = true
			break
		}
	}
	if !consumesJson {
		return nil, errors.Errorf("swagger function discovery uses content type application/json; "+
			"available: %v", swaggerSpec.Consumes)
	}
	// TODO: when response transformation is done, look at produces as well
	var funcs []*v1.Function
	for functionPath, pathItem := range swaggerSpec.Paths.Paths {
		funcs = append(funcs, createFunctionsForPath(swaggerSpec.BasePath, functionPath, pathItem.PathItemProps, swaggerSpec.Definitions)...)
	}
	return funcs, nil
}

func createFunctionsForPath(basePath, functionPath string, path spec.PathItemProps, definitions spec.Definitions) []*v1.Function {
	var pathFunctions []*v1.Function
	appendFunction := func(method string, operation *spec.Operation) {
		pathFunctions = append(pathFunctions, createFunctionForOpertaion(method, basePath, functionPath, operation.OperationProps, definitions))
	}
	if path.Get != nil {
		appendFunction("GET", path.Get)
	}
	if path.Put != nil {
		appendFunction("PUT", path.Put)
	}
	if path.Post != nil {
		appendFunction("POST", path.Post)
	}
	if path.Delete != nil {
		appendFunction("DELETE", path.Delete)
	}
	if path.Options != nil {
		appendFunction("OPTIONS", path.Options)
	}
	if path.Head != nil {
		appendFunction("HEAD", path.Head)
	}
	if path.Patch != nil {
		appendFunction("PATCH", path.Patch)
	}
	return pathFunctions
}

func createFunctionForOpertaion(method string, basePath, functionPath string, operation spec.OperationProps, definitions spec.Definitions) *v1.Function {
	var queryParams, headerParams []string
	//bodyParams := make(map[string]spec.SchemaProps)
	var body string
	for _, param := range operation.Parameters {
		// sort parameters by the template they will go into
		switch param.In {
		case "query":
			queryParams = append(queryParams, fmt.Sprintf("%v={{%v}}", param.Name, param.Name))
		case "header":
			headerParams = append(headerParams, param.Name)
		case "path":
			// nothing to do here, we already get the template
		case "formData":
			log.Warnf("form data params not currently supported; ignoring")
		case "body":
			body = getBodyTemplate("", definitions[param.Name].SchemaProps, definitions)
			//bodyParams[param.Name] = param.Schema.SchemaProps
		}
	}

	path := swaggerPathToJinjaTemplate(basePath + functionPath)
	if len(queryParams) > 0 {
		path += "?" + strings.Join(queryParams, "&")
	}

	headersTemplate := map[string]string{":method": method}
	if body != "" {
		headersTemplate["Content-Type"] = "application/json"
	}
	for _, name := range headerParams {
		headersTemplate[name] = fmt.Sprintf("{{%v}}", name)
	}

	fnName := operation.ID
	if fnName == "" {
		fnName = strings.ToLower(method) + strings.Replace(functionPath, "/", ".", -1)
	}

	return &v1.Function{
		Name: fnName,
		Spec: rest.EncodeFunctionSpec(rest.Template{
			Path:   path,
			Header: headersTemplate,
			Body:   &body,
		}),
	}
}

func getBodyTemplate(parent string, schema spec.SchemaProps, definitions spec.Definitions) string {
	bodyTemplate := "{"
	var fields []string
	for key, prop := range schema.Properties {
		var defaultValue string
		if prop.Default != nil {
			defaultValue = fmt.Sprintf("%v", prop.Default)
		}
		def := getDefinitionFor(prop.Ref, definitions)
		defaultValue = fmt.Sprintf("\"%v\"", defaultValue)
		paramName := "%v"
		if parent != "" {
			paramName = parent + ".%v"
		}
		switch {
		case def != nil:
			if def.Type.Contains("string") {
				fields = append(fields, fmt.Sprintf(`"%v": "{{ default(`+paramName+`, %v)}}"`, key, getBodyTemplate(parent+"."+key, def.SchemaProps, definitions), defaultValue))
			} else {
				fields = append(fields, fmt.Sprintf(`"%v": {{ default(`+paramName+`, %v) }}`, key, getBodyTemplate(parent+"."+key, def.SchemaProps, definitions), defaultValue))
			}
		case prop.Type.Contains("string"):
			// string needs escaping
			fields = append(fields, fmt.Sprintf(`"%v": "{{ default(`+paramName+`, %v)}}"`, key, key, defaultValue))
		default:
			fields = append(fields, fmt.Sprintf(`"%v": {{ default(`+paramName+`, %v) }}`, key, key, defaultValue))
		}
	}
	// idempotency
	sort.SliceStable(fields, func(i, j int) bool {
		return fields[i] < fields[j]
	})
	bodyTemplate += strings.Join(fields, ",")
	bodyTemplate += "}"
	return bodyTemplate
}

func getDefinitionFor(ref spec.Ref, definitions spec.Definitions) *spec.Schema {
	refName := strings.TrimPrefix(ref.String(), "#/definitions/")
	schema, ok := definitions[refName]
	if !ok {
		return nil
	}
	return &schema
}

func swaggerPathToJinjaTemplate(path string) string {
	path = strings.Replace(path, "{", "{{", -1)
	path = strings.Replace(path, "}", "}}", -1)
	return path
}

func getSwaggerSpecForUpsrteam(us *v1.Upstream) (*spec.Swagger, error) {
	annotations, err := swagger.GetSwaggerAnnotations(us)
	if err != nil {
		return nil, errors.Wrapf(err, "invalid or missing swagger annotations on %v", us.Name)
	}
	switch {
	case annotations.SwaggerURL != "":
		return swagger.RetrieveSwaggerDocFromUrl(annotations.SwaggerURL)
	case annotations.InlineSwaggerDoc != "":
		return swagger.ParseSwaggerDoc([]byte(annotations.InlineSwaggerDoc))
	}
	return nil, errors.Errorf("one of %v or %v must be specified on the swagger upstream annotations",
		swagger.AnnotationKeySwaggerDoc,
		swagger.AnnotationKeySwaggerURL)
}
