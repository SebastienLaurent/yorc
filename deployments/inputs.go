// Copyright 2018 Bull S.A.S. Atos Technologies - Bull, Rue Jean Jaures, B.P.68, 78340, Les Clayes-sous-Bois, France.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package deployments

import (
	"context"

	"github.com/pkg/errors"
	"github.com/ystia/yorc/v4/tosca"
)

// GetInputValue tries to retrieve the value of the given input name.
//
// GetInputValue first checks if a non-empty field value exists for this input, if it doesn't then it checks for a non-empty field default.
// If none of them exists then it returns an empty string.
func GetInputValue(ctx context.Context, inputs map[string]tosca.ParameterDefinition,
	deploymentID, inputName string, nestedKeys ...string) (string, error) {

	var err error
	var paramDef *tosca.ParameterDefinition
	paramDefVal, found := inputs[inputName]
	if found {
		paramDef = &paramDefVal
	} else {
		// No such input parameter in execution context, getting it in topology
		found, paramDef, err = getParameterDefinition(ctx, deploymentID, inputName, "inputs")
		if err != nil || !found {
			return "", err
		}
	}

	dataType := getTypeFromParamDefinition(ctx, paramDef)
	// Check first value
	result, err := getValueAssignment(ctx, deploymentID, "", "", "", dataType, paramDef.Value, nestedKeys...)
	if err != nil {
		return "", err
	}
	// Check next default value
	if result == nil {
		result, err = getValueAssignment(ctx, deploymentID, "", "", "", dataType, paramDef.Default, nestedKeys...)
		if err != nil {
			return "", err
		}
	}

	if result == nil {
		return "", errors.Wrapf(err, "Failed to get input %q value", inputName)
	}
	return result.RawString(), nil
}
