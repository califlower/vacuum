// Copyright 2020-2021 Dave Shanley / Quobix
// SPDX-License-Identifier: MIT

package openapi

import (
	"fmt"
	"github.com/daveshanley/vacuum/model"
	"github.com/daveshanley/vacuum/utils"
	"gopkg.in/yaml.v3"
	"regexp"
)

// PathParameters is a rule that checks path level and operation level parameters for correct paths. The rule is
// one of the more complex ones, so here is a little detail as to what is happening.
//-- normalize paths to replace vars with %
//-- check for duplicate paths based on param placement
//-- check for duplicate param names in paths
//-- check for any unknown params (no name)
//-- check if required is set, that it's set to true only.
//-- check no duplicate params
//-- operation paths only
//-- all params in path must be defined
//-- all defined path params must be in path.
type PathParameters struct {
}

// GetSchema returns a model.RuleFunctionSchema defining the schema of the PathParameters rule.
func (pp PathParameters) GetSchema() model.RuleFunctionSchema {
	return model.RuleFunctionSchema{
		Name: "path_parameters",
	}
}

// RunRule will execute the PathParameters rule, based on supplied context and a supplied []*yaml.Node slice.
func (pp PathParameters) RunRule(nodes []*yaml.Node, context model.RuleFunctionContext) []model.RuleFunctionResult {

	if len(nodes) <= 0 {
		return nil
	}

	var results []model.RuleFunctionResult

	opNodes := context.Index.GetPathsNode()
	paramRegex := `(\{;?\??[a-zA-Z0-9_-]+\*?\})`
	rx, _ := regexp.Compile(paramRegex)

	// check for duplicate paths
	seenPaths := make(map[string]string)

	var currentPath string
	var currentVerb string
	pathElements := make(map[string]bool)
	topLevelParams := make(map[string]map[string][]string)

	for j, operationNode := range opNodes.Content {

		if utils.IsNodeStringValue(operationNode) {
			// replace any params with an invalid char (%) so we can perform a path
			// equality check. /hello/{fresh} and /hello/{fish} are equivalent to OpenAPI.
			currentPath = operationNode.Value
			currentPathNormalized := rx.ReplaceAllString(currentPath, "%")

			// check if it's been seen
			if seenPaths[currentPathNormalized] != "" {
				res := model.BuildFunctionResultString(
					fmt.Sprintf("Paths '%s' and '%s' must not be equivalent, paths must be unique",
						seenPaths[currentPathNormalized], currentPath))
				res.StartNode = operationNode
				res.EndNode = operationNode
				res.Path = fmt.Sprintf("$.paths.%s", currentPath)
				res.Rule = context.Rule
				results = append(results, res)
			} else {
				seenPaths[currentPathNormalized] = currentPath
			}

			// check if the value has been used multiple times, 100 segments seems overly cautious.
			for _, pathParam := range rx.FindAllString(currentPath, 100) {
				// strip off curly brackets
				strRx, _ := regexp.Compile(`[{}?*;]`)
				param := strRx.ReplaceAllString(pathParam, "")
				if pathElements[param] {
					res := model.BuildFunctionResultString(
						fmt.Sprintf("Path '%s' must not use the parameter '%s' multiple times",
							currentPath, param))
					res.StartNode = operationNode
					res.EndNode = operationNode
					res.Path = fmt.Sprintf("$.paths.%s", currentPath)
					res.Rule = context.Rule
					results = append(results, res)
				} else {
					pathElements[param] = true
				}

			}
		}

		if utils.IsNodeMap(operationNode) {

			topLevelParametersNode := context.Index.GetOperationParameterReferences()[currentPath]["top"]

			//_, topLevelParametersNode := utils.FindKeyNodeTop("parameters", operationNode.Content)

			// look for top level params
			if topLevelParametersNode != nil {
				for x, topLevelParam := range topLevelParametersNode {
					_, paramInNode := utils.FindKeyNode("in", topLevelParam.Node.Content)
					_, paramRequiredNode := utils.FindKeyNode("required", topLevelParam.Node.Content)
					_, paramNameNode := utils.FindKeyNode("name", topLevelParam.Node.Content)

					if currentVerb == "" {
						currentVerb = "top"
					}

					if pp.isPathParamNamedAndRequired(paramInNode, paramRequiredNode, paramNameNode,
						currentPath, currentVerb, &topLevelParams, nil, &results, context) {

						var paramData map[string][]string
						if topLevelParams["top"] != nil {
							paramData = topLevelParams["top"]
						} else {
							paramData = make(map[string][]string)
						}
						path := []string{"paths", currentPath, "parameters", fmt.Sprintf("%v", x)}
						paramData[paramNameNode.Value] = path
						topLevelParams["top"] = paramData
					}
				}
			}

			// look for verb level params.
			c := 0
			verbLevelParams := make(map[string]map[string][]string)

			for _, verbMapNode := range operationNode.Content {
				if utils.IsNodeStringValue(verbMapNode) && utils.IsHttpVerb(verbMapNode.Value) {
					currentVerb = verbMapNode.Value
				} else {
					continue
				}

				// use index to locate params.
				verbParametersNode := context.Index.GetOperationParameterReferences()[currentPath][currentVerb]

				if verbParametersNode != nil {
					for _, verbParam := range verbParametersNode {

						_, paramInNode := utils.FindKeyNode("in", verbParam.Node.Content)
						_, paramRequiredNode := utils.FindKeyNode("required", verbParam.Node.Content)
						_, paramNameNode := utils.FindKeyNode("name", verbParam.Node.Content)

						if pp.isPathParamNamedAndRequired(paramInNode, paramRequiredNode, paramNameNode,
							currentPath, currentVerb, &verbLevelParams, topLevelParams["top"], &results, context) {

							path := []string{"paths", currentPath, currentVerb, "parameters",
								fmt.Sprintf("%v", c)}
							var paramData map[string][]string
							if verbLevelParams[currentVerb] != nil {
								paramData = verbLevelParams[currentVerb]
							} else {
								paramData = make(map[string][]string)
							}
							paramData[paramNameNode.Value] = path
							verbLevelParams[currentVerb] = paramData
						}
						c++
					}
				}
			}

			// blend together all our params and check they all match up!
			allPathParams := make(map[string]map[string][]string, len(topLevelParams)+len(verbLevelParams))
			if len(topLevelParams) > 0 {
				for k, v := range topLevelParams {
					allPathParams[k] = v
				}
			}
			if len(verbLevelParams) > 0 {
				for k, v := range verbLevelParams {
					allPathParams[k] = v
				}
			}

			startNode := operationNode
			endNode := utils.FindLastChildNode(startNode)
			if j+1 < len(opNodes.Content) {
				endNode = opNodes.Content[j+1]
			}
			pp.ensureAllDefinedPathParamsAreUsedInPath(currentPath, allPathParams,
				pathElements, &results, startNode, endNode, context)
			pp.ensureAllExpectedParamsInPathAreDefined(currentPath, allPathParams,
				pathElements, &results, startNode, endNode, context)

			// reset for the next run.
			pathElements = make(map[string]bool)
			topLevelParams = make(map[string]map[string][]string)
			verbLevelParams = make(map[string]map[string][]string)

		}

	}

	// include operation param errors found by the index
	errors := context.Index.GetOperationParametersIndexErrors()
	for _, err := range errors {
		res := model.BuildFunctionResultString(err.Error.Error())
		res.StartNode = err.Node
		res.EndNode = err.Node
		res.Path = err.Path
		res.Rule = context.Rule
		results = append(results, res)
	}

	return results

}

func (pp PathParameters) ensureAllDefinedPathParamsAreUsedInPath(path string, allPathParams map[string]map[string][]string,
	pathElements map[string]bool, results *[]model.RuleFunctionResult, startNode, endNode *yaml.Node,
	context model.RuleFunctionContext) {

	for _, item := range allPathParams {

		for param := range item {
			foundInElements := false
			for e := range pathElements {
				if param == e {
					foundInElements = true
				}
			}
			if !foundInElements {
				err := fmt.Sprintf("parameter '%s' must be used in path '%s'", param, path)
				res := model.BuildFunctionResultString(err)
				res.StartNode = startNode
				res.EndNode = endNode
				res.Path = fmt.Sprintf("$.paths.%s", path)
				res.Rule = context.Rule
				*results = append(*results, res)
			}
		}
	}
}

func (pp PathParameters) ensureAllExpectedParamsInPathAreDefined(path string, allPathParams map[string]map[string][]string,
	pathElements map[string]bool, results *[]model.RuleFunctionResult, startNode, endNode *yaml.Node,
	context model.RuleFunctionContext) {
	var top map[string][]string

	if allPathParams != nil {
		top = allPathParams["top"]
	}
	for k, e := range allPathParams {
		if k == "top" {
			continue
		}
		for p := range pathElements {
			if !pp.segmentExistsInPathParams(p, e, top) {
				err := fmt.Sprintf("Operation must define parameter '%s' as expected by path '%s'", p, path)
				res := model.BuildFunctionResultString(err)
				res.StartNode = startNode
				res.EndNode = endNode
				res.Path = fmt.Sprintf("$.paths.%s", path)
				res.Rule = context.Rule
				*results = append(*results, res)
			}
		}
	}
}

func (pp PathParameters) segmentExistsInPathParams(segment string, params, top map[string][]string) bool {
	for k, _ := range params {
		if k == segment {
			return true
		}
	}
	for k, _ := range top {
		if k == segment {
			return true
		}
	}
	return false
}

func (pp PathParameters) isPathParamNamed(in, name *yaml.Node) bool {
	if in == nil || name == nil {
		return false
	}
	if in.Value != "path" {
		return false
	}
	return true
}

func (pp PathParameters) isPathParamNamedAndRequired(in, required, name *yaml.Node, currentPath, currentVerb string,
	seenNodes *map[string]map[string][]string, topNodes map[string][]string, results *[]model.RuleFunctionResult,
	context model.RuleFunctionContext) bool {
	if !pp.isPathParamNamed(in, name) {
		return false
	}
	// check if required is set, if so that it's also a bool
	if required != nil {

		var errMsg string
		if currentVerb == "top" {
			errMsg = fmt.Sprintf("%s must have 'required' parameter that is set to 'true'",
				currentPath)
		} else {
			errMsg = fmt.Sprintf("%s %s must have 'required' parameter that is set to 'true'",
				currentPath, currentVerb)
		}

		res := model.BuildFunctionResultString(errMsg)
		res.StartNode = required
		res.EndNode = required
		res.Path = fmt.Sprintf("$.paths.%s.%s.parameters", currentPath, currentVerb)
		res.Rule = context.Rule

		if utils.IsNodeBoolValue(required) {
			if required.Value != "true" {
				*results = append(*results, res)
			}
		} else {
			*results = append(*results, res)
		}
	}
	return true
}
