/* Copyright 2025 Google LLC
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
// limitations under the License.*/

package assessment

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/GoogleCloudPlatform/spanner-migration-tool/logger"
	"github.com/dominikbraun/graph"
	"go.uber.org/zap"
	"golang.org/x/tools/go/packages"
)

// DependencyAnalyzer defines the interface for dependency analysis
type DependencyAnalyzer interface {
	getDependencyGraph(directory string) map[string]map[string]struct{}
	IsDAO(filePath string, fileContent string) bool
	GetFrameworkFromFileContent(fileContent string) string
	GetExecutionOrder(projectDir string) (map[string]map[string]struct{}, [][]string)
	LogDependencyGraph(dependencyGraphmap map[string]map[string]struct{}, projectDir string)
	LogExecutionOrder(groupedTasks [][]string)
}

// BaseAnalyzer provides default implementation for execution order
type BaseAnalyzer struct{}

// GoDependencyAnalyzer implements DependencyAnalyzer for Go projects
type GoDependencyAnalyzer struct {
	BaseAnalyzer
}

func validateGoroot() error {

	goroot := os.Getenv("GOROOT")
	if len(goroot) == 0 {
		return fmt.Errorf("please set GOROOT path to GO version 1.22.7 or higher to ensure that app assessment works")
	}
	return nil
}

// packagesLoadLogger: debug logger for packages.Load function
func packagesLoadLogger(format string, args ...interface{}) {
	logger.Log.Debug(fmt.Sprintf(format, args...))
}

func (b *BaseAnalyzer) RemoveCycle(fileDependenciesMapWithCycle map[string]map[string]struct{}) map[string]map[string]struct{} {

	dependencyGraphCycleCheck := graph.New(graph.StringHash, graph.Directed(), graph.PreventCycles())
	// Dependency graph: key = file, value = list of files it depends on
	dependencyGraph := make(map[string]map[string]struct{})
	for file, dependencies := range fileDependenciesMapWithCycle {
		if _, ok := dependencyGraph[file]; !ok {
			dependencyGraph[file] = make(map[string]struct{})
			dependencyGraphCycleCheck.AddVertex(file)
		}
		for dependency := range dependencies {

			if _, ok := dependencyGraph[dependency]; !ok {
				dependencyGraph[dependency] = make(map[string]struct{})
				dependencyGraphCycleCheck.AddVertex(dependency)
			}

			if dependencyGraphCycleCheck.AddEdge(file, dependency) != nil {
				logger.Log.Debug("Cycle detected: ",
					zap.String("file", file), zap.String("dependency", dependency))
			} else if _, exists := dependencyGraph[file][dependency]; !exists {
				dependencyGraph[file][dependency] = struct{}{}
			}
		}
	}

	return dependencyGraph
}

func (g *GoDependencyAnalyzer) getDependencyGraph(directory string) map[string]map[string]struct{} {

	err := validateGoroot()
	if err != nil {
		logger.Log.Warn("Error validating GOROOT: ", zap.Error(err))
	}
	cfg := &packages.Config{
		Mode: packages.NeedName | packages.NeedFiles | packages.NeedSyntax | packages.NeedTypes | packages.NeedTypesInfo,
		Dir:  (directory),
		Logf: packagesLoadLogger,
	}

	logger.Log.Debug(fmt.Sprintf("loading packages from directory: %s", directory))
	pkgs, err := packages.Load(cfg, "./...")

	if err != nil {
		logger.Log.Fatal("Error loading packages: ", zap.Error(err))
	}

	// Dependency graph: key = file, value = list of files it depends on
	dependencyGraphWithCycles := make(map[string]map[string]struct{})

	// Iterate through all packages and process their files
	for _, pkg := range pkgs {
		if pkg.TypesInfo == nil {
			continue
		}

		// Process symbol usages (functions, variables, structs)
		for ident, obj := range pkg.TypesInfo.Uses {
			if obj != nil && obj.Pos().IsValid() {
				useFile := pkg.Fset.Position(ident.Pos()).Filename

				// Only process files inside the project directory
				if strings.HasPrefix(useFile, directory) {
					// Get the file where the symbol is defined
					defFile := pkg.Fset.Position(obj.Pos()).Filename

					// Only add if the file is inside the project directory and avoid redundant edges
					if strings.HasPrefix(defFile, directory) && useFile != defFile {
						// Initialize the map for the useFile if not present
						if _, ok := dependencyGraphWithCycles[useFile]; !ok {
							dependencyGraphWithCycles[useFile] = make(map[string]struct{})
						}

						if _, ok := dependencyGraphWithCycles[defFile]; !ok {
							dependencyGraphWithCycles[defFile] = make(map[string]struct{})
						}

						dependencyGraphWithCycles[useFile][defFile] = struct{}{}
					}
				}
			}
		}
	}

	return g.RemoveCycle(dependencyGraphWithCycles)
}

func (g *GoDependencyAnalyzer) IsDAO(filePath string, fileContent string) bool {
	filePath = strings.ToLower(filePath)
	if strings.Contains(filePath, "/dao/") {
		return true
	}

	if strings.Contains(fileContent, "database/sql") || strings.Contains(fileContent, "github.com/go-sql-driver/mysql") {
		return true
	}

	if strings.Contains(fileContent, "*sql.DB") || strings.Contains(fileContent, "*sql.Tx") {
		return true
	}

	if strings.Contains(fileContent, "`gorm:\"") {
		return true
	}

	return false
}

func (g *GoDependencyAnalyzer) GetFrameworkFromFileContent(fileContent string) string {
	if strings.Contains(fileContent, "database/sql") || strings.Contains(fileContent, "github.com/go-sql-driver/mysql") {
		return "database/sql"
	}
	if strings.Contains(fileContent, "*sql.DB") || strings.Contains(fileContent, "*sql.Tx") {
		return "database/sql"
	}
	if strings.Contains(fileContent, "`gorm:\"") {
		return "gorm"
	}
	return ""
}

func (g *GoDependencyAnalyzer) GetExecutionOrder(projectDir string) (map[string]map[string]struct{}, [][]string) {
	G := g.getDependencyGraph(projectDir)

	sortedTasks, err := g.TopologicalSort(G)
	if err != nil {
		logger.Log.Debug("Graph still has cycles after relaxation. Sorting not possible: ", zap.Error(err))
		return nil, nil
	}

	logger.Log.Debug("Execution order determined successfully.")
	return G, sortedTasks
}

// AnalyzerFactory creates DependencyAnalyzer instances
func AnalyzerFactory(language string, ctx context.Context) DependencyAnalyzer {
	switch language {
	case "go":
		return &GoDependencyAnalyzer{}
	case "java":
		return &JavaDependencyAnalyzer{ctx: ctx}

	default:
		panic("Unsupported language")
	}
}

func (b *BaseAnalyzer) TopologicalSort(G map[string]map[string]struct{}) ([][]string, error) {
	inDegree := make(map[string]int)
	for node := range G {
		inDegree[node] = 0
	}
	var maxDegree int

	for node := range G {
		for neighbor := range G[node] {
			inDegree[neighbor]++
			if inDegree[neighbor] > maxDegree {
				maxDegree = inDegree[neighbor]
			}
		}
	}

	taskLevels := make([][]string, maxDegree+1)

	for node, degree := range inDegree {
		degree = maxDegree - degree
		taskLevels[degree] = append(taskLevels[degree], node)
	}

	return taskLevels, nil
}

func (b *BaseAnalyzer) LogDependencyGraph(dependencyGraph map[string]map[string]struct{}, projectDir string) {

	logger.Log.Debug("Dependency Graph:")
	for file, dependencies := range dependencyGraph {
		logger.Log.Debug("depends on: ", zap.String("filepath: ", strings.TrimPrefix(file, projectDir)))
		for dep := range dependencies {
			logger.Log.Debug("Dependency: ", zap.String("filepath", strings.TrimPrefix(dep, projectDir)))
		}
	}
}

func (b *BaseAnalyzer) LogExecutionOrder(groupedTasks [][]string) {

	logger.Log.Debug("Execution Order Groups:")
	for i, group := range groupedTasks {
		logger.Log.Debug("Level: ", zap.Int("level", i), zap.String("group", strings.Join(group, ", ")))
	}
}
