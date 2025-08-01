/*
	Copyright 2025 Google LLC

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
*/
package assessment

import (
	"bufio"
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"cloud.google.com/go/vertexai/genai"
	assessment "github.com/GoogleCloudPlatform/spanner-migration-tool/assessment/collectors/embeddings"
	parser "github.com/GoogleCloudPlatform/spanner-migration-tool/assessment/collectors/parser"
	dependencyAnalyzer "github.com/GoogleCloudPlatform/spanner-migration-tool/assessment/collectors/project_analyzer"
	utils "github.com/GoogleCloudPlatform/spanner-migration-tool/assessment/utils"
	"github.com/GoogleCloudPlatform/spanner-migration-tool/common/task"
	"github.com/GoogleCloudPlatform/spanner-migration-tool/logger"
	"go.uber.org/zap"
)

//go:embed prompts/analyze-code-prompt.txt
var analyzeCodePromptTemplate string

//go:embed prompts/dao-migration-prompt.txt
var daoMigrationPromptTemplate string

//go:embed prompts/non-dao-migration-prompt.txt
var nonDAOMigrationPromptTemplate string

// generativeModel defines an interface for interacting with a generative AI model.
type generativeModel interface {
	GenerateContent(ctx context.Context, parts ...genai.Part) (*genai.GenerateContentResponse, error)
	SetResponseMIMEType(string)
}

// genaiModelWrapper wraps the actual genai.GenerativeModel to implement the generativeModel interface.
type genaiModelWrapper struct {
	*genai.GenerativeModel
}

// SetResponseMIMEType sets the response MIME type for the underlying model.
func (w *genaiModelWrapper) SetResponseMIMEType(mimeType string) {
	w.GenerativeModel.ResponseMIMEType = mimeType
}

// MigrationCodeSummarizer holds the LLM models and configurations for code migration assessment.
type MigrationCodeSummarizer struct {
	gcpProjectID               string
	gcpLocation                string
	aiClient                   *genai.Client
	geminiProModel             generativeModel
	geminiFlashModel           generativeModel
	codeSampleDatabase         *assessment.MysqlConceptDb
	querySampleDatabase        *assessment.MysqlConceptDb
	sourceDatabaseFramework    string
	targetDatabaseFramework    string
	projectDependencyAnalyzer  dependencyAnalyzer.DependencyAnalyzer
	projectProgrammingLanguage string
	sourceDatabaseSchema       string
	targetDatabaseSchema       string
	projectRootPath            string
	dependencyGraph            map[string]map[string]struct{}
	fileDependencyAnalysis     map[string]FileDependencyInfo
}

// FileDependencyInfo stores dependency analysis data for a single file.
type FileDependencyInfo struct {
	PublicMethodSignatures []any
	IsDAODependent         bool
}

// FileAnalysisResponse represents the response after analyzing a single file.
type FileAnalysisResponse struct {
	CodeAssessment      *utils.CodeAssessment
	MethodSignatures    []any
	AnalyzedProjectPath string
	AnalyzedFilePath    string
	QueryResults        []utils.QueryTranslationResult
}

// FileAnalysisInput represents the input for analyzing a single file.
type FileAnalysisInput struct {
	Context       context.Context
	ProjectPath   string
	FilePath      string
	MethodChanges string
	FileContent   string
	FileIndex     int
}

// LLMQuestionOutput represents the expected JSON output for asking clarifying questions.
type LLMQuestionOutput struct {
	Questions []string `json:"questions"`
}
type FrameworkPair struct {
	Source string
	Target string
}

const jsonParserRetryAttempts = 3

var SupportedProgrammingLanguages = map[string]bool{
	"go":   true,
	"java": true,
}

var SupportedFrameworkCombinations = map[FrameworkPair]bool{
	{Source: "jdbc", Target: "jdbc"}:                            true,
	{Source: "hibernate", Target: "hibernate"}:                  true,
	{Source: "go-sql-driver/mysql", Target: "go-sql-spanner"}:   true,
	{Source: "vertx-mysql-client", Target: "vertx-jdbc-client"}: true,
	// Add more allowed combinations here
}

// NewMigrationCodeSummarizer initializes a new MigrationCodeSummarizer.
// ToDo:Add Unit Tests
func NewMigrationCodeSummarizer(
	ctx context.Context,
	googleGenerativeAIAPIKey *string,
	projectID, location, sourceSchema, targetSchema, projectPath, language, sourceFramework, targetFramework string,
) (*MigrationCodeSummarizer, error) {

	if language == "" {
		logger.Log.Info("source code programming language info missing. detecting from source code...")
		language = detectProgrammingLanguage(projectPath)
		logger.Log.Info("detected programming language: " + language)
	}

	if isProgrammingLanguageSupported(language, SupportedProgrammingLanguages) == false {
		return nil, fmt.Errorf("programming language '%s' not supported. Supported languages are: %v", language, SupportedProgrammingLanguages)
	}

	projectDependencyAnalyzer := dependencyAnalyzer.AnalyzerFactory(language, ctx)

	if sourceFramework == "" {
		logger.Log.Info("source code framework info missing. detecting from source code...")
		sourceFramework = GetDatabaseSourceFramework(projectPath, language, projectDependencyAnalyzer)
		logger.Log.Info("detected source framework: " + sourceFramework)
	}

	if targetFramework == "" {
		logger.Log.Info("target framework is not specified. assuming source framework as target framework")
		targetFramework = sourceFramework
	}

	if isFrameworkCombinationSupported(sourceFramework, targetFramework, SupportedFrameworkCombinations) == false {
		return nil, fmt.Errorf("source-target framework '%s'-'%s' combination not supported. Supported frameworks are: %v", sourceFramework, targetFramework, SupportedFrameworkCombinations)
	}

	if googleGenerativeAIAPIKey != nil {
		os.Setenv("GOOGLE_API_KEY", *googleGenerativeAIAPIKey)
	}

	client, err := genai.NewClient(ctx, projectID, location)
	if err != nil {
		return nil, fmt.Errorf("failed to create Vertex AI client: %w", err)
	}

	codeSampleDB, err := assessment.NewMysqlToSpannerCodeDb(projectID, location, strings.ToLower(sourceFramework)+"_"+strings.ToLower(targetFramework))
	if err != nil {
		return nil, fmt.Errorf("failed to load code sample DB: %w", err)
	}

	querySampleDB, err := assessment.NewMysqlToSpannerQueryDb(projectID, location)
	if err != nil {
		return nil, fmt.Errorf("failed to load MySQL query sample DB: %w", err)
	}

	summarizer := &MigrationCodeSummarizer{
		gcpProjectID:               projectID,
		gcpLocation:                location,
		aiClient:                   client,
		geminiProModel:             &genaiModelWrapper{client.GenerativeModel("gemini-1.5-pro-002")},
		geminiFlashModel:           &genaiModelWrapper{client.GenerativeModel("gemini-2.0-flash-001")},
		codeSampleDatabase:         codeSampleDB,
		projectDependencyAnalyzer:  projectDependencyAnalyzer,
		sourceDatabaseSchema:       sourceSchema,
		sourceDatabaseFramework:    strings.ToUpper(sourceFramework),
		targetDatabaseFramework:    strings.ToUpper(targetFramework),
		querySampleDatabase:        querySampleDB,
		targetDatabaseSchema:       targetSchema,
		projectRootPath:            projectPath,
		projectProgrammingLanguage: language,
		dependencyGraph:            make(map[string]map[string]struct{}),
		fileDependencyAnalysis:     make(map[string]FileDependencyInfo),
	}
	summarizer.geminiFlashModel.SetResponseMIMEType("application/json")
	summarizer.geminiProModel.SetResponseMIMEType("application/json")

	return summarizer, nil
}

// InvokeCodeConversion performs code conversion using the LLM.
func (m *MigrationCodeSummarizer) InvokeCodeConversion(
	ctx context.Context,
	originalPrompt, sourceCode, olderSchema, newSchema, identifier string,
) (string, error) {
	prompt := analyzeCodePromptTemplate
	prompt = strings.ReplaceAll(prompt, "{{SOURCE_FRAMEWORK}}", m.sourceDatabaseFramework)
	prompt = strings.ReplaceAll(prompt, "{{TARGET_FRAMEWORK}}", m.targetDatabaseFramework)
	prompt = strings.ReplaceAll(prompt, "{{SOURCE_CODE}}", sourceCode)
	prompt = strings.ReplaceAll(prompt, "{{OLDER_SCHEMA}}", olderSchema)
	prompt = strings.ReplaceAll(prompt, "{{NEW_SCHEMA}}", newSchema)

	response, err := utils.GenerateContentWithRetry(ctx, m.geminiFlashModel.(*genaiModelWrapper).GenerativeModel, genai.Text(prompt), 5, logger.Log)
	if err != nil {
		return "", err
	}
	logger.Log.Debug("LLM Token Usage (Initial Conversion): ",
		zap.Int32("Prompt Tokens", response.UsageMetadata.PromptTokenCount),
		zap.Int32("Candidate Tokens", response.UsageMetadata.CandidatesTokenCount),
		zap.Int32("Total Tokens", response.UsageMetadata.TotalTokenCount))

	var llmResponse string
	if response.Candidates != nil && len(response.Candidates) > 0 && len(response.Candidates[0].Content.Parts) > 0 {
		if part, ok := response.Candidates[0].Content.Parts[0].(genai.Text); ok {
			llmResponse = string(part)
		}
	}

	llmResponse = m.parseJSONWithRetries(m.geminiFlashModel, prompt, llmResponse, identifier)

	var questionOutput LLMQuestionOutput
	err = json.Unmarshal([]byte(llmResponse), &questionOutput) // Convert JSON string to struct
	if err != nil {
		logger.Log.Debug("Error unmarshalling LLM question output: ", zap.Error(err))
	}

	finalPrompt := originalPrompt
	if len(questionOutput.Questions) > 0 {
		codeSearchResults := make([][]string, len(questionOutput.Questions))
		querySearchResults := make([][]string, len(questionOutput.Questions))
		answersPresent := false

		for i, question := range questionOutput.Questions {
			// Search in code samples database
			relevantRecords := m.codeSampleDatabase.Search([]string{question}, m.gcpProjectID, m.gcpLocation, 0.25, 2)
			if len(relevantRecords) > 0 {
				answersPresent = true
				for _, record := range relevantRecords {
					if rewrite, ok := record["rewrite"].(string); ok {
						codeSearchResults[i] = append(codeSearchResults[i], rewrite)
					} else {
						logger.Log.Debug("Error: 'rewrite' field in code sample DB is not a string")
					}
				}
			}

			// Search in MySQL query samples database
			queryRecords := m.querySampleDatabase.Search([]string{question}, m.gcpProjectID, m.gcpLocation, 0.25, 2)
			if len(queryRecords) > 0 {
				answersPresent = true
				for _, record := range queryRecords {
					if rewrite, ok := record["rewrite"].(string); ok {
						querySearchResults[i] = append(querySearchResults[i], rewrite)
					} else {
						logger.Log.Debug("Error: 'rewrite' field in query DB is not a string")
					}
				}
			}
		}

		if answersPresent {
			formattedResults := formatQuestionsAndSearchResults(questionOutput.Questions, codeSearchResults, querySearchResults)
			finalPrompt += "\n" + formattedResults
		}
	}

	finalResponse, err := utils.GenerateContentWithRetry(ctx, m.geminiProModel.(*genaiModelWrapper).GenerativeModel, genai.Text(finalPrompt), 5, logger.Log)
	if err != nil {
		logger.Log.Error("Error generating final content:", zap.Error(err))
		return "", err
	}
	logger.Log.Debug("LLM Token Usage (Final Conversion): ",
		zap.Int32("Prompt Tokens", finalResponse.UsageMetadata.PromptTokenCount),
		zap.Int32("Candidate Tokens", finalResponse.UsageMetadata.CandidatesTokenCount),
		zap.Int32("Total Tokens", finalResponse.UsageMetadata.TotalTokenCount))

	if len(finalResponse.Candidates) > 0 && len(finalResponse.Candidates[0].Content.Parts) > 0 {
		if part, ok := finalResponse.Candidates[0].Content.Parts[0].(genai.Text); ok {
			llmResponse = string(part)
		}
	}

	logger.Log.Debug("Final LLM Response: ", zap.String("response", llmResponse))

	llmResponse = m.parseJSONWithRetries(m.geminiProModel, finalPrompt, llmResponse, identifier)

	return llmResponse, nil
}

func formatQuestionsAndSearchResults(questions []string, codeSearchResults [][]string, querySearchResults [][]string) string {
	formattedString := "Use the following questions and their corresponding answers to guide the code conversions:\n**Clarifying Questions and Potential Solutions:**\n\n"

	for i, question := range questions {
		if len(codeSearchResults[i]) > 0 || len(querySearchResults[i]) > 0 {
			formattedString += fmt.Sprintf("* **Question %d:** %s\n", i+1, question)
			for j, result := range codeSearchResults[i] {
				formattedString += fmt.Sprintf("  * **Potential Code Solution %d:** %s\n", j+1, result)
			}
			for j, result := range querySearchResults[i] {
				formattedString += fmt.Sprintf("  * **Potential Query Solution %d:** %s\n", j+1, result)
			}
		}
	}

	return formattedString
}

func (m *MigrationCodeSummarizer) parseJSONWithRetries(model generativeModel, originalPrompt string, originalResponse string, identifier string) string {
	jsonFixPromptTemplate := `
        You are a JSON parser expert tasked with fixing parsing errors in JSON string. Golang's json.Unmarshal library is
        being used for parsing the json string. The following JSON string is currently failing with error message: %s.
        Ensure that all the parsing errors are resolved and output string is parsable by json.Unmarshal library. Also,
        ensure that the output only contain JSON string.
        
        %s
        `

	for i := 0; i < jsonParserRetryAttempts; i++ {
		logger.Log.Debug("JSON Parsing Retry - Original Response: ", zap.String("response", originalResponse))
		trimmedResponse := strings.TrimSpace(originalResponse)

		if trimmedResponse == "" {
			return trimmedResponse
		}

		trimmedResponse = strings.TrimPrefix(trimmedResponse, "```json\n")
		trimmedResponse = strings.TrimPrefix(trimmedResponse, "@@@json\n")
		trimmedResponse = strings.TrimSuffix(trimmedResponse, "```")
		trimmedResponse = strings.TrimSuffix(trimmedResponse, "@@@")
		trimmedResponse = strings.ReplaceAll(trimmedResponse, "\t", "")
		trimmedResponse = strings.TrimSpace(trimmedResponse)

		var result map[string]any
		err := json.Unmarshal([]byte(trimmedResponse), &result)
		if err == nil {
			logger.Log.Debug("JSON Parsing Successful - Parsed Response: ", zap.String("response", trimmedResponse))
			return trimmedResponse
		}

		logger.Log.Debug("JSON Parsing Error: ", zap.Error(err))

		newPrompt := fmt.Sprintf(jsonFixPromptTemplate, err.Error(), trimmedResponse)

		logger.Log.Debug("JSON Parsing Retry Prompt: ", zap.String("prompt", newPrompt))
		resp, err := model.GenerateContent(context.Background(), genai.Text(newPrompt))
		if err != nil {
			logger.Log.Fatal("Failed to get response from LLM for JSON parsing retry: ", zap.Error(err))
		}
		logger.Log.Debug("LLM Token Usage (JSON Parsing Retry): ",
			zap.Int32("Prompt Tokens", resp.UsageMetadata.PromptTokenCount),
			zap.Int32("Candidate Tokens", resp.UsageMetadata.CandidatesTokenCount),
			zap.Int32("Total Tokens", resp.UsageMetadata.TotalTokenCount))
		if len(resp.Candidates) > 0 && len(resp.Candidates[0].Content.Parts) > 0 {
			if part, ok := resp.Candidates[0].Content.Parts[0].(genai.Text); ok {
				originalResponse = string(part)
			}
		}
	}
	logger.Log.Warn("Failed to parse JSON after multiple retries for identifier: ", zap.String("identifier", identifier), zap.String("originalResponse", originalResponse))
	return ""
}

func (m *MigrationCodeSummarizer) fetchFileContent(filepath string) (string, error) {
	content, err := utils.ReadFileWithExplicitBuffer(filepath, bufio.MaxScanTokenSize*10)
	if err != nil {
		logger.Log.Fatal("Failed to read file: ", zap.Error(err), zap.String("filepath", filepath))
		return "", err
	}
	return content, nil
}

// AnalyzeFileTask wraps the AnalyzeFile function to be used with the task runner.
// ToDo:Add Unit Tests
func (m *MigrationCodeSummarizer) AnalyzeFileTask(analyzeFileInput *FileAnalysisInput, mutex *sync.Mutex) task.TaskResult[*FileAnalysisResponse] {
	analyzeFileResponse := m.AnalyzeFile(
		analyzeFileInput.Context,
		analyzeFileInput.ProjectPath,
		analyzeFileInput.FilePath,
		analyzeFileInput.MethodChanges,
		analyzeFileInput.FileContent,
		analyzeFileInput.FileIndex)
	return task.TaskResult[*FileAnalysisResponse]{Result: analyzeFileResponse, Err: nil}
}

// AnalyzeFile analyzes a single file to identify potential migration issues.
func (m *MigrationCodeSummarizer) AnalyzeFile(ctx context.Context, projectPath, filepath, methodChanges, content string, fileIndex int) *FileAnalysisResponse {
	emptySnippets := make([]utils.Snippet, 0)
	emptyAssessment := &utils.CodeAssessment{
		Snippets:        &emptySnippets,
		GeneralWarnings: make([]string, 0),
	}

	codeAssessment := emptyAssessment
	var llmResponse string
	var isDataAccessObject bool
	extractedMethodSignatures := make([]any, 0)
	var queryResults []utils.QueryTranslationResult

	if m.projectDependencyAnalyzer.IsDAO(filepath, content) {
		logger.Log.Debug("Analyzing DAO File: ", zap.String("filepath", filepath))
		var err error
		prompt := m.getPromptForDAOClass(content, filepath, &methodChanges, &m.sourceDatabaseSchema, &m.targetDatabaseSchema)
		llmResponse, err = m.InvokeCodeConversion(ctx, prompt, content, m.sourceDatabaseSchema, m.targetDatabaseSchema, "analyze-dao-class-"+filepath)
		isDataAccessObject = true
		if err != nil {
			logger.Log.Error("Error analyzing DAO class: ", zap.Error(err))
			return &FileAnalysisResponse{codeAssessment, extractedMethodSignatures, projectPath, filepath, queryResults}
		}

		if llmResponse != "" {
			publicMethods, err := m.extractPublicMethodSignatures(llmResponse)
			if err != nil {
				logger.Log.Error("Error extracting public method signatures from DAO analysis response: ", zap.Error(err))
			} else {
				extractedMethodSignatures = publicMethods
			}
		}

	} else {
		logger.Log.Debug("Analyzing Non-DAO File: ", zap.String("filepath", filepath))
		prompt := m.getPromptForNonDAOClass(content, filepath, &methodChanges)
		response, err := utils.GenerateContentWithRetry(ctx, m.geminiFlashModel.(*genaiModelWrapper).GenerativeModel, genai.Text(prompt), 5, logger.Log)

		if err != nil {
			return &FileAnalysisResponse{codeAssessment, extractedMethodSignatures, projectPath, filepath, queryResults}
		}
		logger.Log.Debug("LLM Token Usage (Non-DAO Analysis): ",
			zap.Int32("Prompt Tokens", response.UsageMetadata.PromptTokenCount),
			zap.Int32("Candidate Tokens", response.UsageMetadata.CandidatesTokenCount),
			zap.Int32("Total Tokens", response.UsageMetadata.TotalTokenCount))

		if response.Candidates != nil && len(response.Candidates) > 0 && len(response.Candidates[0].Content.Parts) > 0 {
			if part, ok := response.Candidates[0].Content.Parts[0].(genai.Text); ok {
				llmResponse = string(part)
			}
		}

		llmResponse = m.parseJSONWithRetries(m.geminiFlashModel, prompt, llmResponse, "analyze-non-dao-class-"+filepath)
		isDataAccessObject = false

		if llmResponse != "" {
			methodSignatures, err := m.extractPublicMethodSignatures(llmResponse)
			if err != nil {
				logger.Log.Error("Error extracting method signatures from Non-DAO analysis response: ", zap.Error(err))
			} else {
				extractedMethodSignatures = methodSignatures
			}
		}

	}
	logger.Log.Debug("File Analysis LLM Response: ", zap.String("response", llmResponse))

	codeAssessment, queryResults, err := parser.ParseFileAnalyzerResponse(projectPath, filepath, llmResponse, isDataAccessObject, fileIndex)

	if err != nil {
		return &FileAnalysisResponse{emptyAssessment, extractedMethodSignatures, projectPath, filepath, queryResults}
	}

	return &FileAnalysisResponse{codeAssessment, extractedMethodSignatures, projectPath, filepath, queryResults}
}

func (m *MigrationCodeSummarizer) extractPublicMethodSignatures(fileAnalysisResponse string) ([]any, error) {
	var responseMap map[string]any

	err := json.Unmarshal([]byte(fileAnalysisResponse), &responseMap)
	if err != nil {
		logger.Log.Error("Error unmarshalling file analysis response for public method signatures: ", zap.Error(err))
		return nil, err
	}

	// Try top-level (non-DAO)
	if publicMethodChanges, ok := responseMap["method_signature_changes"].([]any); ok {
		return publicMethodChanges, nil
	}

	// Try inside code_changes (DAO)
	if codeChanges, ok := responseMap["code_changes"].([]any); ok {
		var allMethodChanges []any
		for _, ccRaw := range codeChanges {
			if cc, ok := ccRaw.(map[string]any); ok {
				if methodChanges, ok := cc["method_signature_changes"].(map[string]any); ok {
					allMethodChanges = append(allMethodChanges, methodChanges)
				}
			}
		}
		return allMethodChanges, nil
	}

	return []any{}, nil
}

func (m *MigrationCodeSummarizer) fetchDependentMethodSignatureChange(filePath string) string {
	dependentMethodSignatures := make([]any, 0, 10)
	for dependency := range m.dependencyGraph[filePath] {
		if dependencyInfo, ok := m.fileDependencyAnalysis[dependency]; ok {
			dependentMethodSignatures = append(dependentMethodSignatures, dependencyInfo.PublicMethodSignatures...)
		}
	}

	dependentMethodSignatureJSON, err := json.MarshalIndent(dependentMethodSignatures, "", "  ")
	if err != nil {
		logger.Log.Error("Error marshalling dependent method signatures: ", zap.Error(err))
		return ""
	}
	return string(dependentMethodSignatureJSON)
}

func (m *MigrationCodeSummarizer) analyzeFileDependencies(filePath, fileContent string) (bool, string) {
	if m.projectDependencyAnalyzer.IsDAO(filePath, fileContent) {
		return true, m.fetchDependentMethodSignatureChange(filePath)
	}

	dependsOnDAO := false
	for dependency := range m.dependencyGraph[filePath] {
		if dependencyInfo, ok := m.fileDependencyAnalysis[dependency]; ok && dependencyInfo.IsDAODependent {
			dependsOnDAO = true
			break
		}
	}

	if dependsOnDAO {
		return true, m.fetchDependentMethodSignatureChange(filePath)
	}

	return false, ""
}

// AnalyzeProject orchestrates the analysis of the entire project.
// ToDo:Add Unit Tests
func (m *MigrationCodeSummarizer) AnalyzeProject(ctx context.Context) (*utils.CodeAssessment, []utils.QueryTranslationResult, error) {
	logger.Log.Info(fmt.Sprintf("analyzing project: %s", m.projectRootPath))
	dependencyGraph, processingOrder := m.projectDependencyAnalyzer.GetExecutionOrder(m.projectRootPath)
	m.projectDependencyAnalyzer.LogDependencyGraph(dependencyGraph, m.projectRootPath)
	m.projectDependencyAnalyzer.LogExecutionOrder(processingOrder)

	m.dependencyGraph = dependencyGraph

	var allSnippets []utils.Snippet
	projectCodeAssessment := &utils.CodeAssessment{
		ProjectPath:     m.projectRootPath,
		Snippets:        &allSnippets,
		GeneralWarnings: make([]string, 0, 10),
	}

	parallelTaskRunner := &task.RunParallelTasksImpl[*FileAnalysisInput, *FileAnalysisResponse]{}
	fileIndex := 0
	totalLinesOfCode := 0
	projectProgrammingLanguage := m.projectProgrammingLanguage
	detectedFramework := m.sourceDatabaseFramework

	logger.Log.Info("initiating file scanning and analysis. this may take a few minutes.")
	var allQueryResults []utils.QueryTranslationResult
	for _, fileBatch := range processingOrder {
		analysisInputs := make([]*FileAnalysisInput, 0, len(fileBatch))
		for _, filePath := range fileBatch {
			fileIndex++
			fileContent, err := m.fetchFileContent(filePath)
			if err != nil {
				logger.Log.Error("Error fetching file content: ", zap.Error(err))
				continue
			}
			totalLinesOfCode += strings.Count(fileContent, "\n")

			isDependentOnDAO, methodChanges := m.analyzeFileDependencies(filePath, fileContent)
			if !isDependentOnDAO {
				continue
			}
			analysisInputs = append(analysisInputs, &FileAnalysisInput{
				Context:       ctx,
				ProjectPath:   m.projectRootPath,
				FilePath:      filePath,
				MethodChanges: methodChanges,
				FileContent:   fileContent,
				FileIndex:     fileIndex,
			})
		}

		if len(analysisInputs) == 0 {
			continue
		}

		analysisResults, err := parallelTaskRunner.RunParallelTasks(analysisInputs, utils.PARALLEL_TASK_RUNNER_COUNT, m.AnalyzeFileTask, false)
		if err != nil {
			logger.Log.Error("Error running parallel file analysis: ", zap.Error(err))
		} else {
			for _, result := range analysisResults {
				analysisResponse := result.Result
				logger.Log.Debug("File Code Assessment Result: ",
					zap.Any("codeAssessment", analysisResponse.CodeAssessment),
					zap.String("filePath", analysisResponse.AnalyzedFilePath))

				*projectCodeAssessment.Snippets = append(*projectCodeAssessment.Snippets, *analysisResponse.CodeAssessment.Snippets...)
				projectCodeAssessment.GeneralWarnings = append(projectCodeAssessment.GeneralWarnings, analysisResponse.CodeAssessment.GeneralWarnings...)

				m.fileDependencyAnalysis[analysisResponse.AnalyzedFilePath] = FileDependencyInfo{
					PublicMethodSignatures: analysisResponse.MethodSignatures,
					IsDAODependent:         true,
				}
				allQueryResults = append(allQueryResults, analysisResponse.QueryResults...)
			}
		}
	}

	projectCodeAssessment.Language = projectProgrammingLanguage
	projectCodeAssessment.Framework = detectedFramework
	projectCodeAssessment.TotalLoc = totalLinesOfCode
	projectCodeAssessment.TotalFiles = fileIndex
	return projectCodeAssessment, allQueryResults, nil
}

func isProgrammingLanguageSupported(programmingLanguage string, supportedProgrammingLanguages map[string]bool) bool {
	_, exists := supportedProgrammingLanguages[strings.ToLower(programmingLanguage)]
	return exists
}

func isFrameworkCombinationSupported(sourceFramework, targetFramework string, supportedCombinations map[FrameworkPair]bool) bool {
	pair := FrameworkPair{
		Source: strings.ToLower(sourceFramework),
		Target: strings.ToLower(targetFramework),
	}
	_, exists := supportedCombinations[pair]
	return exists
}

func detectProgrammingLanguage(projectPath string) string {
	languageCounts := make(map[string]int)

	err := filepath.Walk(projectPath, func(filePath string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() {
			if strings.HasSuffix(filePath, ".go") {
				languageCounts["go"]++
			} else if strings.HasSuffix(filePath, ".py") {
				languageCounts["python"]++
			} else if strings.HasSuffix(filePath, ".java") {
				languageCounts["java"]++
			} else if strings.HasSuffix(filePath, ".js") || strings.HasSuffix(filePath, ".jsx") {
				languageCounts["javascript"]++
			}
			// Add more language-specific checks as needed
		}
		return nil
	})

	if err != nil {
		logger.Log.Error("Error walking the path: ", zap.Error(err))
		return ""
	}

	if len(languageCounts) > 0 {
		languages := make([]string, 0, len(languageCounts))
		for lang := range languageCounts {
			languages = append(languages, lang)
		}
		sort.Strings(languages)
		logger.Log.Info("repository  is using following programming languages: " + strings.Join(languages, ", "))
	}

	var dominantLanguage string
	maxCount := 0
	for lang, count := range languageCounts {
		if count > maxCount {
			maxCount = count
			dominantLanguage = lang
		}
	}
	return dominantLanguage
}

// Generic function to get the dominant database framework using a FrameworkDetector.
func GetDatabaseSourceFramework(projectRoot string, language string, projectDependencyAnalyzer dependencyAnalyzer.DependencyAnalyzer) string {
	frameworkCounts := make(map[string]int)
	fileExtension := language

	filepath.Walk(projectRoot, func(filePath string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() && strings.HasSuffix(strings.ToLower(filePath), fileExtension) {
			contentBytes, err := os.ReadFile(filePath)
			if err != nil {
				return err
			}
			fileContent := string(contentBytes)
			framework := projectDependencyAnalyzer.GetFrameworkFromFileContent(fileContent)
			if framework != "" {
				frameworkCounts[framework]++
			}
		}
		return nil
	})

	var dominantFramework string
	maxCount := 0
	for framework, count := range frameworkCounts {
		if count > maxCount {
			maxCount = count
			dominantFramework = framework
		}
	}

	return dominantFramework
}

func (m *MigrationCodeSummarizer) getPromptForNonDAOClass(content, filepath string, methodChanges *string) string {
	prompt := nonDAOMigrationPromptTemplate
	prompt = strings.ReplaceAll(prompt, "{{FILEPATH}}", filepath)
	prompt = strings.ReplaceAll(prompt, "{{CONTENT}}", content)
	prompt = strings.ReplaceAll(prompt, "{{METHOD_CHANGES}}", *methodChanges)
	prompt = strings.ReplaceAll(prompt, "{{SOURCE_FRAMEWORK}}", m.sourceDatabaseFramework)
	prompt = strings.ReplaceAll(prompt, "{{TARGET_FRAMEWORK}}", m.targetDatabaseFramework)
	return prompt
}

func (m *MigrationCodeSummarizer) getPromptForDAOClass(content, filepath string, methodChanges, oldSchema, newSchema *string) string {
	prompt := daoMigrationPromptTemplate
	prompt = strings.ReplaceAll(prompt, "{{OLDER_SCHEMA}}", *oldSchema)
	prompt = strings.ReplaceAll(prompt, "{{NEW_SCHEMA}}", *newSchema)
	prompt = strings.ReplaceAll(prompt, "{{FILEPATH}}", filepath)
	prompt = strings.ReplaceAll(prompt, "{{CONTENT}}", content)
	prompt = strings.ReplaceAll(prompt, "{{METHOD_CHANGES}}", *methodChanges)
	prompt = strings.ReplaceAll(prompt, "{{SOURCE_FRAMEWORK}}", m.sourceDatabaseFramework)
	prompt = strings.ReplaceAll(prompt, "{{TARGET_FRAMEWORK}}", m.targetDatabaseFramework)
	return prompt
}
