// Copyright 2022 Google LLC
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

package table

import (
	"fmt"
	"regexp"

	"github.com/GoogleCloudPlatform/spanner-migration-tool/common/constants"
	"github.com/GoogleCloudPlatform/spanner-migration-tool/internal"
	"github.com/GoogleCloudPlatform/spanner-migration-tool/sources/common"
	"github.com/GoogleCloudPlatform/spanner-migration-tool/spanner/ddl"
	"github.com/GoogleCloudPlatform/spanner-migration-tool/webv2/session"
)

const (
	NotNullAdded   string = "ADDED"
	NotNullRemoved string = "REMOVED"
)

var SpannerToCassandra = map[string]string{
	ddl.Bool:     "boolean",
	ddl.Bytes:    "blob",
	ddl.Date:     "date",
	ddl.Float32:  "float",
	ddl.Float64:  "double",
	ddl.Int64:    "bigint",
	ddl.Numeric:  "decimal",
	ddl.String:   "text",
	ddl.Timestamp:"timestamp",
}

// GetCassandraType returns default cassandra type for specified Spanner type
func GetCassandraType(spannerType string) string {
	if cassandraType, ok := SpannerToCassandra[spannerType]; ok {
		return cassandraType
	}
	return ""
}

// IsColumnPresentInColNames check column is present in colnames.
func IsColumnPresentInColNames(colIds []string, colId string) bool {

	for _, id := range colIds {
		if id == colId {
			return true
		}
	}

	return false
}

// GetSpannerTableDDL return Spanner Table DDL as string.
func GetSpannerTableDDL(spannerTable ddl.CreateTable, spDialect string, driver string) string {
	sessionState := session.GetSessionState()
	c := ddl.Config{Comments: true, ProtectIds: false, SpDialect: spDialect, Source: driver}

	ddl := spannerTable.PrintCreateTable(sessionState.Conv.SpSchema, c)

	return ddl
}

func renameInterleaveTableSchema(interleaveTableSchema []InterleaveTableSchema, tableName, colId, oldName, newName, colType string, colSize int) []InterleaveTableSchema {

	tableIndex := isTablePresent(interleaveTableSchema, tableName)

	interleaveTableSchema = createInterleaveTableSchema(interleaveTableSchema, tableName, tableIndex)

	interleaveTableSchema = renameInterleaveColumn(interleaveTableSchema, tableName, colId, oldName, newName, colType, colSize)

	return interleaveTableSchema
}

func isTablePresent(interleaveTableSchema []InterleaveTableSchema, table string) int {

	for i := 0; i < len(interleaveTableSchema); i++ {
		if interleaveTableSchema[i].Table == table {
			return i
		}
	}

	return -1
}

func createInterleaveTableSchema(interleaveTableSchema []InterleaveTableSchema, table string, tableIndex int) []InterleaveTableSchema {

	if tableIndex == -1 {
		itc := InterleaveTableSchema{}
		itc.Table = table
		itc.InterleaveColumnChanges = []InterleaveColumn{}

		interleaveTableSchema = append(interleaveTableSchema, itc)
	}

	return interleaveTableSchema
}

func renameInterleaveColumn(interleaveTableSchema []InterleaveTableSchema, table, columnId, colName, newName, colType string, colSize int) []InterleaveTableSchema {
	tableIndex := isTablePresent(interleaveTableSchema, table)

	colIndex := isColumnPresent(interleaveTableSchema[tableIndex].InterleaveColumnChanges, columnId)

	interleaveTableSchema = createInterleaveColumn(interleaveTableSchema, tableIndex, colIndex, columnId, colName, newName, colType, colSize)

	if tableIndex != -1 && colIndex != -1 {
		interleaveTableSchema[tableIndex].InterleaveColumnChanges[colIndex].ColumnId = columnId
		interleaveTableSchema[tableIndex].InterleaveColumnChanges[colIndex].ColumnName = colName
		interleaveTableSchema[tableIndex].InterleaveColumnChanges[colIndex].UpdateColumnName = newName
		interleaveTableSchema[tableIndex].InterleaveColumnChanges[colIndex].Type = colType
		interleaveTableSchema[tableIndex].InterleaveColumnChanges[colIndex].Size = colSize
	}

	return interleaveTableSchema

}

func createInterleaveColumn(interleaveTableSchema []InterleaveTableSchema, tableIndex int, colIndex int, columnId, colName, newName, colType string, colSize int) []InterleaveTableSchema {

	if colIndex == -1 {

		if columnId != "" {

			interleaveColumn := InterleaveColumn{}
			interleaveColumn.ColumnId = columnId
			interleaveColumn.ColumnName = colName
			interleaveColumn.UpdateColumnName = newName
			interleaveColumn.Type = colType
			interleaveColumn.Size = colSize

			interleaveTableSchema[tableIndex].InterleaveColumnChanges = append(interleaveTableSchema[tableIndex].InterleaveColumnChanges, interleaveColumn)
		}
	}

	return interleaveTableSchema
}

func isColumnPresent(interleaveColumn []InterleaveColumn, columnId string) int {

	for i := 0; i < len(interleaveColumn); i++ {
		if interleaveColumn[i].ColumnId == columnId {
			return i
		}
	}

	return -1
}

func updateTypeOfInterleaveTableSchema(interleaveTableSchema []InterleaveTableSchema, table, columnId, colName, previousType, updateType string, previousSize, newSize int) []InterleaveTableSchema {

	tableIndex := isTablePresent(interleaveTableSchema, table)

	interleaveTableSchema = createInterleaveTableSchema(interleaveTableSchema, table, tableIndex)

	interleaveTableSchema = updateTypeOfInterleaveColumn(interleaveTableSchema, table, columnId, colName, previousType, updateType, previousSize, newSize)
	return interleaveTableSchema
}

func updateTypeOfInterleaveColumn(interleaveTableSchema []InterleaveTableSchema, table, columnId, colName, previousType, updateType string, previousSize, newSize int) []InterleaveTableSchema {

	tableIndex := isTablePresent(interleaveTableSchema, table)
	colIndex := isColumnPresent(interleaveTableSchema[tableIndex].InterleaveColumnChanges, columnId)
	interleaveTableSchema = createInterleaveColumnType(interleaveTableSchema, tableIndex, colIndex, columnId, colName, previousType, updateType, previousSize, newSize)

	if tableIndex != -1 && colIndex != -1 {
		interleaveTableSchema[tableIndex].InterleaveColumnChanges[colIndex].ColumnId = columnId
		if interleaveTableSchema[tableIndex].InterleaveColumnChanges[colIndex].ColumnName == "" {
			interleaveTableSchema[tableIndex].InterleaveColumnChanges[colIndex].ColumnName = colName
		}
		interleaveTableSchema[tableIndex].InterleaveColumnChanges[colIndex].Type = previousType
		interleaveTableSchema[tableIndex].InterleaveColumnChanges[colIndex].UpdateType = updateType
		interleaveTableSchema[tableIndex].InterleaveColumnChanges[colIndex].Size = previousSize
		interleaveTableSchema[tableIndex].InterleaveColumnChanges[colIndex].UpdateSize = newSize
	}

	return interleaveTableSchema
}

func updateInterleaveTableSchemaForChangeInSize(interleaveTableSchema []InterleaveTableSchema, table, columnId, colName, colType string, previousSize, newSize int) []InterleaveTableSchema {
	tableIndex := isTablePresent(interleaveTableSchema, table)
	interleaveTableSchema = createInterleaveTableSchema(interleaveTableSchema, table, tableIndex)
	interleaveTableSchema = updateInterleaveColumnForChangeInSize(interleaveTableSchema, table, columnId, colName, colType, previousSize, newSize)
	return interleaveTableSchema
}

func updateInterleaveColumnForChangeInSize(interleaveTableSchema []InterleaveTableSchema, table, columnId, colName, colType string, previousSize, newSize int) []InterleaveTableSchema {
	tableIndex := isTablePresent(interleaveTableSchema, table)
	colIndex := isColumnPresent(interleaveTableSchema[tableIndex].InterleaveColumnChanges, columnId)
	interleaveTableSchema = createInterleaveColumnType(interleaveTableSchema, tableIndex, colIndex, columnId, colName, colType, colType, previousSize, newSize)
	if tableIndex != -1 && colIndex != -1 {
		interleaveTableSchema[tableIndex].InterleaveColumnChanges[colIndex].ColumnId = columnId
		if interleaveTableSchema[tableIndex].InterleaveColumnChanges[colIndex].ColumnName == "" {
			interleaveTableSchema[tableIndex].InterleaveColumnChanges[colIndex].ColumnName = colName
		}
		if interleaveTableSchema[tableIndex].InterleaveColumnChanges[colIndex].Type == "" {
			interleaveTableSchema[tableIndex].InterleaveColumnChanges[colIndex].Type = colType
		}
		interleaveTableSchema[tableIndex].InterleaveColumnChanges[colIndex].Size = previousSize
		interleaveTableSchema[tableIndex].InterleaveColumnChanges[colIndex].UpdateSize = newSize
	}
	return interleaveTableSchema
}

func createInterleaveColumnType(interleaveTableSchema []InterleaveTableSchema, tableIndex int, colIndex int, columnId, colName, previousType, updateType string, previousSize, newSize int) []InterleaveTableSchema {

	if colIndex == -1 {
		if columnId != "" {
			interleaveColumn := InterleaveColumn{}
			interleaveColumn.ColumnId = columnId
			interleaveColumn.ColumnName = colName
			interleaveColumn.Type = previousType
			interleaveColumn.UpdateType = updateType
			interleaveColumn.Size = previousSize
			interleaveColumn.UpdateSize = newSize
			interleaveTableSchema[tableIndex].InterleaveColumnChanges = append(interleaveTableSchema[tableIndex].InterleaveColumnChanges, interleaveColumn)
		}
	}

	return interleaveTableSchema
}

func trimRedundantInterleaveTableSchema(interleaveTableSchema []InterleaveTableSchema) []InterleaveTableSchema {
	updatedInterleaveTableSchema := []InterleaveTableSchema{}
	for _, v := range interleaveTableSchema {
		if len(v.InterleaveColumnChanges) > 0 {
			updatedInterleaveTableSchema = append(updatedInterleaveTableSchema, v)
		}
	}
	return updatedInterleaveTableSchema
}

func updateInterleaveTableSchema(conv *internal.Conv, interleaveTableSchema []InterleaveTableSchema) []InterleaveTableSchema {
	for k, v := range interleaveTableSchema {
		for ind, col := range v.InterleaveColumnChanges {
			if col.UpdateColumnName == "" {
				interleaveTableSchema[k].InterleaveColumnChanges[ind].UpdateColumnName = col.ColumnName
			}
			if col.UpdateType == "" {
				interleaveTableSchema[k].InterleaveColumnChanges[ind].UpdateType = col.Type
			}
			if col.UpdateSize == 0 {
				interleaveTableSchema[k].InterleaveColumnChanges[ind].UpdateSize = col.Size
			}
		}
	}
	return interleaveTableSchema
}

func UpdateNotNull(notNullChange, tableId, colId string, conv *internal.Conv) {

	sp := conv.SpSchema[tableId]

	switch notNullChange {
	case NotNullAdded:
		spColDef := sp.ColDefs[colId]
		spColDef.NotNull = true
		sp.ColDefs[colId] = spColDef
	case NotNullRemoved:
		spColDef := sp.ColDefs[colId]
		spColDef.NotNull = false
		sp.ColDefs[colId] = spColDef
	}
}

func UpdateAutoGenCol(autoGen ddl.AutoGenCol, tableId, colId string, conv *internal.Conv) map[string]ddl.Sequence {
	sp := conv.SpSchema[tableId]
	sequences := conv.SpSequences
	spColDef := sp.ColDefs[colId]
	if spColDef.AutoGen.GenerationType == constants.SEQUENCE {
		seqId := getSequenceId(spColDef.AutoGen.Name, conv.SpSequences)
		sequences = deleteColumnFromSequence(seqId, tableId, colId, sequences)
	}
	spColDef.AutoGen = autoGen
	sp.ColDefs[colId] = spColDef
	if autoGen.GenerationType == constants.SEQUENCE {
		seqId := getSequenceId(autoGen.Name, conv.SpSequences)
		sequences = addColumnToSequence(seqId, tableId, colId, sequences)
	}
	conv.SpSchema[tableId] = sp
	return sequences
}

func getFkColumnPosition(colIds []string, colId string) int {
	for i, id := range colIds {
		if colId == id {
			return i
		}
	}
	return -1
}

func isColFistOderPk(pks []ddl.IndexKey, colId string) bool {
	for _, pk := range pks {
		if pk.ColId == colId && pk.Order == 1 {
			return true
		}
	}
	return false
}

func deleteColumnFromSequence(sequenceId string, tableId, colId string, sequences map[string]ddl.Sequence) map[string]ddl.Sequence {
	if _, ok := sequences[sequenceId].ColumnsUsingSeq[tableId]; ok {
		cols := sequences[sequenceId].ColumnsUsingSeq[tableId]
		for i, col := range cols {
			if col == colId {
				sequences[sequenceId].ColumnsUsingSeq[tableId] = append(cols[:i], cols[i+1:]...)
				return sequences
			}
		}
	}
	return sequences
}

func addColumnToSequence(sequenceId string, tableId, colId string, sequences map[string]ddl.Sequence) map[string]ddl.Sequence {
	if _, ok := sequences[sequenceId].ColumnsUsingSeq[tableId]; ok {
		cols := sequences[sequenceId].ColumnsUsingSeq[tableId]
		found := false
		for _, c := range cols {
			if c == colId {
				found = true
				fmt.Println(found)
				return sequences
			}
		}
		if !found {
			sequences[sequenceId].ColumnsUsingSeq[tableId] = append(cols, colId)
			return sequences
		}
	} else {
		cols := []string{colId}
		seq := sequences[sequenceId]
		if seq.ColumnsUsingSeq == nil {
			seq.ColumnsUsingSeq = make(map[string][]string)
		}
		seq.ColumnsUsingSeq[tableId] = cols
		sequences[sequenceId] = seq
	}
	return sequences
}

func getSequenceId(sequenceName string, spSeq map[string]ddl.Sequence) string {
	for seqId, seq := range spSeq {
		if seq.Name == sequenceName {
			return seqId
		}
	}
	return ""
}

// Add, deletes and updates default value associated with a column during edit column functionality
func UpdateDefaultValue(dv ddl.DefaultValue, tableId, colId string, conv *internal.Conv) {
	col := conv.SpSchema[tableId].ColDefs[colId]
	if !dv.IsPresent {
		col.DefaultValue = ddl.DefaultValue{}
		conv.SpSchema[tableId].ColDefs[colId] = col
		return
	}

	var expressionId string
	if dv.Value.ExpressionId == "" {
		if _, exists := conv.SrcSchema[tableId]; exists {
			if column, exists := conv.SrcSchema[tableId].ColDefs[colId]; exists {
				if column.DefaultValue.Value.ExpressionId != "" {
					expressionId = column.DefaultValue.Value.ExpressionId
				}
			}
		}
		if expressionId == "" {
			expressionId = internal.GenerateExpressionId()
		}
	} else {
		expressionId = dv.Value.ExpressionId
	}
	re := regexp.MustCompile(`\([^)]*\)`)
	col.DefaultValue = ddl.DefaultValue{
		Value: ddl.Expression{
			ExpressionId: expressionId,
			Statement:    common.SanitizeDefaultValue(dv.Value.Statement, col.T.Name, re.MatchString(dv.Value.Statement)),
		},
		IsPresent: true,
	}
	conv.SpSchema[tableId].ColDefs[colId] = col
}
