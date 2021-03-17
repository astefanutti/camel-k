/*
Licensed to the Apache Software Foundation (ASF) under one or more
contributor license agreements.  See the NOTICE file distributed with
this work for additional information regarding copyright ownership.
The ASF licenses this file to You under the Apache License, Version 2.0
(the "License"); you may not use this file except in compliance with
the License.  You may obtain a copy of the License at

   http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package builder

import (
	"os"

	"github.com/apache/camel-k/pkg/util/camel"
	"github.com/apache/camel-k/pkg/util/kubernetes"
)


func init() {
	registerSteps(Steps)
}

type steps struct {
	CleanUpBuildDir         Step
	GenerateProjectSettings Step
	InjectDependencies      Step
	SanitizeDependencies    Step
	StandardImageContext    Step
	IncrementalImageContext Step
}

var Steps = steps{
	CleanUpBuildDir:         NewStep(ProjectGenerationPhase-1, cleanUpBuildDir),
	GenerateProjectSettings: NewStep(ProjectGenerationPhase+1, generateProjectSettings),
	InjectDependencies:      NewStep(ProjectGenerationPhase+2, injectDependencies),
	SanitizeDependencies:    NewStep(ProjectGenerationPhase+3, sanitizeDependencies),
	StandardImageContext:    NewStep(ApplicationPackagePhase, standardImageContext),
	IncrementalImageContext: NewStep(ApplicationPackagePhase, incrementalImageContext),
}

var DefaultSteps = []Step{
	Steps.CleanUpBuildDir,
	Steps.GenerateProjectSettings,
	Steps.InjectDependencies,
	Steps.SanitizeDependencies,
	Steps.IncrementalImageContext,
}

func cleanUpBuildDir(ctx *builderContext) error {
	if ctx.Build.BuildDir == "" {
		return nil
	}

	return os.RemoveAll(ctx.Build.BuildDir)
}

func generateProjectSettings(ctx *builderContext) error {
	val, err := kubernetes.ResolveValueSource(ctx.C, ctx.Client, ctx.Namespace, &ctx.Build.Maven.Settings)
	if err != nil {
		return err
	}
	if val != "" {
		ctx.Maven.SettingsData = []byte(val)
	}

	return nil
}

func injectDependencies(ctx *builderContext) error {
	// Add dependencies from build
	return camel.ManageIntegrationDependencies(&ctx.Maven.Project, ctx.Build.Dependencies, ctx.Catalog)
}

func sanitizeDependencies(ctx *builderContext) error {
	return camel.SanitizeIntegrationDependencies(ctx.Maven.Project.Dependencies)
}