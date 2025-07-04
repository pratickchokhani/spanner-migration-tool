// Copyright 2024 Google LLC
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

package common

import (
	"github.com/GoogleCloudPlatform/spanner-migration-tool/spanner/ddl"
	"sync"

	"github.com/GoogleCloudPlatform/spanner-migration-tool/common/task"
	"github.com/GoogleCloudPlatform/spanner-migration-tool/internal"
	"github.com/GoogleCloudPlatform/spanner-migration-tool/schema"
	"github.com/stretchr/testify/mock"
)

type MockInfoSchema struct {
	mock.Mock
}

func (mis *MockInfoSchema) GenerateSrcSchema(conv *internal.Conv, infoSchema InfoSchema, numWorkers int) (int, error) {
	args := mis.Called(conv, infoSchema, numWorkers)
	return args.Get(0).(int), args.Error(1)
}
func (mis *MockInfoSchema) ProcessData(conv *internal.Conv, infoSchema InfoSchema, additionalAttributes internal.AdditionalDataAttributes) {
}
func (mis *MockInfoSchema) SetRowStats(conv *internal.Conv, infoSchema InfoSchema) {}
func (mis *MockInfoSchema) processTable(conv *internal.Conv, table SchemaAndName, infoSchema InfoSchema) (schema.Table, error) {
	args := mis.Called(conv, table, infoSchema)
	return args.Get(0).(schema.Table), args.Error(1)
}
func (mis *MockInfoSchema) GetIncludedSrcTablesFromConv(conv *internal.Conv) (schemaToTablesMap map[string]internal.SchemaDetails, err error) {
	args := mis.Called(conv)
	return args.Get(0).(map[string]internal.SchemaDetails), args.Error(1)
}

type MockUtilsOrder struct {
	mock.Mock
}

func (muo *MockUtilsOrder) initPrimaryKeyOrder(conv *internal.Conv) {}

func (muo *MockUtilsOrder) initIndexOrder(conv *internal.Conv) {}

type MockSchemaToSpanner struct {
	mock.Mock
}

func (mss *MockSchemaToSpanner) SchemaToSpannerDDL(conv *internal.Conv, toddl ToDdl, attributes internal.AdditionalSchemaAttributes) error {
	args := mss.Called(conv, toddl, attributes)
	return args.Error(0)
}

func (mss *MockSchemaToSpanner) SchemaToSpannerDDLHelper(conv *internal.Conv, toddl ToDdl, srcTable schema.Table, isRestore bool) error {
	args := mss.Called(conv, toddl, srcTable, isRestore)
	return args.Error(0)
}

func (mss *MockSchemaToSpanner) SchemaToSpannerSequenceHelper(conv *internal.Conv, srcSequence ddl.Sequence) error {
	args := mss.Called(conv, srcSequence)
	return args.Error(0)
}

type MockProcessSchema struct {
	mock.Mock
}

func (mps *MockProcessSchema) ProcessSchema(conv *internal.Conv, infoSchema InfoSchema, numWorkers int, attributes internal.AdditionalSchemaAttributes, s SchemaToSpannerInterface, uo UtilsOrderInterface, is InfoSchemaInterface) error {
	args := mps.Called(conv, infoSchema, numWorkers, attributes, s, uo, is)
	return args.Error(0)
}

type MockRunParallelTasks[I any, O any] struct {
	mock.Mock
}

func (mrpt *MockRunParallelTasks[I, O]) RunParallelTasks(input []I, numWorkers int, f func(i I, mutex *sync.Mutex) task.TaskResult[O],
	fastExit bool) ([]task.TaskResult[O], error) {
	args := mrpt.Called(input, numWorkers, f, fastExit)
	return args.Get(0).([]task.TaskResult[O]), args.Error(1)
}

// MockDbDump is a mock implementation of the DbDump interface.
type MockDbDump struct {
	mock.Mock
}

// GetToDdl provides a mock implementation for GetToDdl.
func (m *MockDbDump) GetToDdl() ToDdl {
	args := m.Called()
	return args.Get(0).(ToDdl)
}

// ProcessDump provides a mock implementation for ProcessDump.
func (m *MockDbDump) ProcessDump(conv *internal.Conv, r *internal.Reader) error {
	args := m.Called(conv, r)
	return args.Error(0)
}

// MockToDdl is a mock implementation of the ToDdl interface.
type MockToDdl struct {
	mock.Mock
}

// ToSpannerType provides a mock implementation for ToSpannerType.
func (m *MockToDdl) ToSpannerType(conv *internal.Conv, spType string, srcType schema.Type, isPk bool) (ddl.Type, []internal.SchemaIssue) {
	args := m.Called(conv, spType, srcType, isPk)
	return args.Get(0).(ddl.Type), args.Get(1).([]internal.SchemaIssue)
}

// GetColumnAutoGen provides a mock implementation for GetColumnAutoGen.
func (m *MockToDdl) GetColumnAutoGen(conv *internal.Conv, autoGenCol ddl.AutoGenCol, colId string, tableId string) (*ddl.AutoGenCol, error) {
	args := m.Called(conv, autoGenCol, colId, tableId)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*ddl.AutoGenCol), args.Error(1)
}

// MockOptionProvider is a mock implementation of OptionProvider
type MockOptionProvider struct {
	mock.Mock
}

// ToSpannerType and GetCOlumnAutoGen are methods of the ToDdl interface
func (m *MockOptionProvider) ToSpannerType(conv *internal.Conv, spType string, srcType schema.Type, isPk bool) (ddl.Type, []internal.SchemaIssue) {
	args := m.Called(conv, spType, srcType, isPk)
	return args.Get(0).(ddl.Type), args.Get(1).([]internal.SchemaIssue)
}

func (m *MockOptionProvider) GetColumnAutoGen(conv *internal.Conv, autoGenCol ddl.AutoGenCol, colId string, tableId string) (*ddl.AutoGenCol, error) {
	args := m.Called(conv, autoGenCol, colId, tableId)
	return args.Get(0).(*ddl.AutoGenCol), args.Error(1)
}

// GetTypeOption is a method of the OptionProvider interface
func (m *MockOptionProvider) GetTypeOption(srcTypeName string, spType ddl.Type) string {
	args := m.Called(srcTypeName, spType)
	return args.String(0)
}