// Copyright 2025 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package mysql

import (
	"database/sql"
	"fmt"
	"math"
	"strings"

	"github.com/GoogleCloudPlatform/spanner-migration-tool/assessment/utils"
	"github.com/GoogleCloudPlatform/spanner-migration-tool/internal"
	"github.com/GoogleCloudPlatform/spanner-migration-tool/schema"
)

type InfoSchemaImpl struct {
	Db     *sql.DB
	DbName string
}

type SourceSpecificComparisonImpl struct{}

func (isi InfoSchemaImpl) GetTableInfo(conv *internal.Conv) (map[string]utils.TableAssessmentInfo, error) {
	tb := make(map[string]utils.TableAssessmentInfo)
	dbIdentifier := utils.DbIdentifier{
		DatabaseName: isi.DbName,
	}
	var errString string
	for _, table := range conv.SrcSchema {
		columnAssessments := make(map[string]utils.ColumnAssessmentInfo[any])
		var collation, charset string
		q := `SELECT TABLE_COLLATION, SUBSTRING_INDEX(TABLE_COLLATION, '_', 1) as CHARACTER_SET
		FROM INFORMATION_SCHEMA.TABLES
		WHERE TABLE_SCHEMA = ? AND TABLE_NAME = ?;`
		err := isi.Db.QueryRow(q, isi.DbName, table.Name).Scan(&collation, &charset)
		if err != nil {
			errString = errString + fmt.Sprintf("couldn't get schema for table %s: %s", table.Name, err)
		}
		for _, column := range table.ColDefs {
			q = `SELECT c.column_type, c.extra, c.generation_expression
              FROM information_schema.COLUMNS c
              where table_schema = ? and table_name = ? and column_name = ? ORDER BY c.ordinal_position;`
			var columnType string
			var colExtra, colGeneratedExp sql.NullString
			var isOnUpdateTimestampSet, isVirtual, isPresent bool
			var generatedColumn utils.GeneratedColumnInfo
			err := isi.Db.QueryRow(q, isi.DbName, table.Name, column.Name).Scan(&columnType, &colExtra, &colGeneratedExp)
			if err != nil {
				errString = errString + fmt.Sprintf("couldn't get schema for column %s.%s: %s", table.Name, column.Name, err)
			}
			if strings.Contains(colExtra.String, "on update CURRENT_TIMESTAMP") {
				isOnUpdateTimestampSet = true
			} else if strings.Contains(colExtra.String, "VIRTUAL GENERATED") {
				isVirtual = true
				isPresent = true
			} else if strings.Contains(colExtra.String, "STORED GENERATED") {
				isPresent = true
			}
			if colGeneratedExp.Valid {
				generatedColumn = utils.GeneratedColumnInfo{
					Statement: colGeneratedExp.String,
					IsPresent: isPresent,
					IsVirtual: isVirtual,
				}
			}
			columnAssessments[column.Id] = utils.ColumnAssessmentInfo[any]{
				Db: utils.DbIdentifier{
					DatabaseName: isi.DbName,
				},
				Name:                   column.Name,
				TableName:              table.Name,
				ColumnDef:              column,
				IsUnsigned:             strings.Contains(strings.ToLower(columnType), " unsigned"),
				MaxColumnSize:          getColumnMaxSize(column.Type.Name, column.Type.Mods, charset),
				IsOnUpdateTimestampSet: isOnUpdateTimestampSet,
				GeneratedColumn:        generatedColumn,
			}
		}
		tb[table.Id] = utils.TableAssessmentInfo{Name: table.Name, TableDef: table, ColumnAssessmentInfos: columnAssessments, Db: dbIdentifier, Charset: charset, Collation: collation}
	}
	if errString != "" {
		return tb, fmt.Errorf(errString)
	}
	return tb, nil
}

// GetIndexes return a list of all indexes for the specified table.
func (isi InfoSchemaImpl) GetIndexInfo(table string, index schema.Index) (utils.IndexAssessmentInfo, error) {
	q := `SELECT DISTINCT INDEX_NAME,COLUMN_NAME,SEQ_IN_INDEX,COLLATION,NON_UNIQUE,INDEX_TYPE
		FROM INFORMATION_SCHEMA.STATISTICS 
		WHERE TABLE_SCHEMA = ?
			AND TABLE_NAME = ?
			AND INDEX_NAME = ?
		ORDER BY INDEX_NAME, SEQ_IN_INDEX;`

	var name, column, sequence, nonUnique, indexType string
	var collation sql.NullString
	err := isi.Db.QueryRow(q, isi.DbName, table, index.Name).Scan(&name, &column, &sequence, &collation, &nonUnique, &indexType)
	if err != nil {
		return utils.IndexAssessmentInfo{}, fmt.Errorf("couldn't get index for index name %s.%s: %s", table, index.Name, err)
	}
	return utils.IndexAssessmentInfo{
		Ty:   indexType,
		Name: name,
		Db: utils.DbIdentifier{
			DatabaseName: isi.DbName,
		},
		IndexDef: index,
	}, nil

}

func (isi InfoSchemaImpl) GetTriggerInfo() ([]utils.TriggerAssessmentInfo, error) {
	q := `SELECT DISTINCT TRIGGER_NAME,EVENT_OBJECT_TABLE,ACTION_STATEMENT,ACTION_TIMING,EVENT_MANIPULATION
	FROM INFORMATION_SCHEMA.TRIGGERS 
	WHERE EVENT_OBJECT_SCHEMA = ?`
	rows, err := isi.Db.Query(q, isi.DbName)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var name, table, actionStmt, actionTiming, eventManipulation string
	var triggers []utils.TriggerAssessmentInfo
	var errString string
	for rows.Next() {
		if err := rows.Scan(&name, &table, &actionStmt, &actionTiming, &eventManipulation); err != nil {
			errString = errString + fmt.Sprintf("Can't scan: %v", err)
			continue
		}
		triggers = append(triggers, utils.TriggerAssessmentInfo{
			Name:              name,
			Operation:         actionStmt,
			TargetTable:       table,
			ActionTiming:      actionTiming,
			EventManipulation: eventManipulation,
			Db: utils.DbIdentifier{
				DatabaseName: isi.DbName,
			},
		})
	}
	if errString != "" {
		return triggers, fmt.Errorf(errString)
	}
	return triggers, nil
}

func (isi InfoSchemaImpl) GetStoredProcedureInfo() ([]utils.StoredProcedureAssessmentInfo, error) {
	q := `SELECT DISTINCT ROUTINE_NAME,ROUTINE_DEFINITION,IS_DETERMINISTIC
	FROM INFORMATION_SCHEMA.ROUTINES 
	WHERE ROUTINE_TYPE='PROCEDURE' AND ROUTINE_SCHEMA = ?`
	rows, err := isi.Db.Query(q, isi.DbName)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var name, defintion, isDeterministic string
	var storedProcedures []utils.StoredProcedureAssessmentInfo
	var errString string
	for rows.Next() {
		if err := rows.Scan(&name, &defintion, &isDeterministic); err != nil {
			errString = errString + fmt.Sprintf("Can't scan: %v", err)
			continue
		}
		storedProcedures = append(storedProcedures, utils.StoredProcedureAssessmentInfo{
			Name:            name,
			Definition:      defintion,
			IsDeterministic: isDeterministic == "YES",
			Db: utils.DbIdentifier{
				DatabaseName: isi.DbName,
			},
		})
	}
	if errString != "" {
		return storedProcedures, fmt.Errorf(errString)
	}
	return storedProcedures, nil
}

func (isi InfoSchemaImpl) GetFunctionInfo() ([]utils.FunctionAssessmentInfo, error) {
	q := `SELECT DISTINCT ROUTINE_NAME,ROUTINE_DEFINITION,IS_DETERMINISTIC, DTD_IDENTIFIER
	FROM INFORMATION_SCHEMA.ROUTINES 
	WHERE ROUTINE_TYPE='FUNCTION' AND ROUTINE_SCHEMA = ?`
	rows, err := isi.Db.Query(q, isi.DbName)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var name, defintion, isDeterministic, datatype string
	var functions []utils.FunctionAssessmentInfo
	var errString string
	for rows.Next() {
		if err := rows.Scan(&name, &defintion, &isDeterministic, &datatype); err != nil {
			errString = errString + fmt.Sprintf("Can't scan: %v", err)
			continue
		}
		functions = append(functions, utils.FunctionAssessmentInfo{
			Name:            name,
			Definition:      defintion,
			IsDeterministic: isDeterministic == "YES",
			Db: utils.DbIdentifier{
				DatabaseName: isi.DbName,
			},
			Datatype: datatype,
		})
	}
	if errString != "" {
		return functions, fmt.Errorf(errString)
	}
	return functions, nil
}

func (isi InfoSchemaImpl) GetViewInfo() ([]utils.ViewAssessmentInfo, error) {
	q := `SELECT DISTINCT TABLE_NAME,VIEW_DEFINITION,CHECK_OPTION, IS_UPDATABLE
	FROM INFORMATION_SCHEMA.VIEWS 
	WHERE TABLE_SCHEMA = ?`
	rows, err := isi.Db.Query(q, isi.DbName)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var name, defintion, checkOption, isUpdatable string
	var views []utils.ViewAssessmentInfo
	var errString string
	for rows.Next() {
		if err := rows.Scan(&name, &defintion, &checkOption, &isUpdatable); err != nil {
			errString = errString + fmt.Sprintf("Can't scan: %v", err)
			continue
		}
		views = append(views, utils.ViewAssessmentInfo{
			Name:        name,
			Definition:  defintion,
			CheckOption: checkOption,
			IsUpdatable: isUpdatable == "YES",
			Db: utils.DbIdentifier{
				DatabaseName: isi.DbName,
			},
		})
	}
	if errString != "" {
		return views, fmt.Errorf(errString)
	}
	return views, nil
}

func getColumnMaxSize(dataType string, mods []int64, mysqlCharset string) int64 {
	dataTypeLower := strings.ToLower(dataType)
	bytesPerChar := int64(1) // Default for binary types or non-char types

	switch dataTypeLower {
	case "char", "varchar", "tinytext", "text", "mediumtext", "longtext":
		bytesPerChar = getMaxBytesPerChar(mysqlCharset)
	}

	switch dataTypeLower {
	case "date":
		return 4
	case "timestamp", "datetime":
		return 8
	case "bit":
		if len(mods) > 0 {
			return int64(math.Ceil(float64(mods[0]) / 8.0))
		}
		return 1
	case "tinyint":
		return 1
	case "smallint":
		return 2
	case "mediumint":
		return 3
	case "int", "integer":
		return 4
	case "bigint":
		return 8
	case "float":
		return 4
	case "double", "real":
		return 8
	case "decimal", "numeric":
		if len(mods) > 0 {
			precision := mods[0]
			scale := int64(0)
			if len(mods) > 1 {
				scale = mods[1]
			}
			// Calculate storage based on precision and scale
			intDigits := precision - scale
			intBytes := (intDigits + 8) / 9
			fracBytes := (scale + 8) / 9

			return intBytes + fracBytes // Total size
		}
		return 8 // Default size if no precision/scale provided

	case "char":
		maxChars := int64(1) // Default for CHAR is CHAR(1)
		if len(mods) > 0 {
			maxChars = mods[0]
		}
		return maxChars * bytesPerChar
	case "varchar":
		maxChars := int64(0)
		if len(mods) > 0 {
			maxChars = mods[0]
		} else {
			maxChars = 255
		}
		return maxChars * bytesPerChar

	case "binary", "varbinary":
		if len(mods) > 0 {
			return mods[0]
		}
		return 255
	case "tinyblob":
		return 255
	case "blob":
		return 65535
	case "mediumblob":
		return 16777215
	case "longblob":
		return 4294967295

	case "tinytext":
		return 255 * bytesPerChar
	case "text":
		return 65535 * bytesPerChar
	case "mediumtext":
		return 16777215 * bytesPerChar
	case "longtext":
		return 4294967295 * bytesPerChar

	case "json":
		return 4294967295
	default:
		return 4
	}
}

func getMaxBytesPerChar(charset string) int64 {
	charsetLower := strings.ToLower(charset)
	switch charsetLower {
	case "binary", "latin1", "ascii", "cp850", "dec8", "hp8", "koi8r", "latin2", "swe7",
		"armscii8", "cp1250", "cp1251", "cp1256", "cp1257", "cp852", "cp866",
		"geostd8", "greek", "hebrew", "keybcs2", "koi8u", "latin5", "latin7", "macce", "macroman":
		return 1
	case "big5", "gb2312", "gbk", "sjis", "ujis", "eucjpms", "euckr", "ucs2":
		return 2
	case "utf8", "utf8mb3":
		return 3
	case "utf8mb4", "utf16", "utf32", "gb18030":
		return 4
	default:
		// Default to a common multi-byte max if unknown.
		return 4
	}
}

func (ssa SourceSpecificComparisonImpl) IsDataTypeCodeCompatible(srcColumnDef utils.SrcColumnDetails, spColumnDef utils.SpColumnDetails) bool {

	switch strings.ToUpper(spColumnDef.Datatype) {
	case "BOOL":
		switch srcColumnDef.Datatype {
		case "tinyint":
			return true
		case "bit":
			return true
		default:
			return false
		}
	case "BYTES":
		switch srcColumnDef.Datatype {
		case "binary":
			return true
		case "varbinary":
			return true
		case "blob":
			return true
		default:
			return false
		}
	case "DATE":
		switch srcColumnDef.Datatype {
		case "date":
			return true
		default:
			return false
		}
	case "FLOAT32":
		switch srcColumnDef.Datatype {
		case "float":
			return true
		case "double":
			return true
		default:
			return false
		}
	case "FLOAT64":
		switch srcColumnDef.Datatype {
		case "float":
			return true
		case "double":
			return true
		default:
			return false
		}
	case "INT64":
		switch srcColumnDef.Datatype {
		case "int":
			return true
		case "bigint":
			return true
		default:
			return false
		}
	case "JSON":
		switch srcColumnDef.Datatype {
		case "json":
			return true
		case "varchar":
			return true
		default:
			return false
		}
	case "NUMERIC":
		switch srcColumnDef.Datatype {
		case "float":
			return true
		case "double":
			return true
		default:
			return false
		}
	case "STRING":
		switch srcColumnDef.Datatype {
		case "varchar":
			return true
		case "text":
			return true
		case "mediumtext":
			return true
		case "longtext":
			return true
		default:
			return false
		}
	case "TIMESTAMP":
		switch srcColumnDef.Datatype {
		case "timestamp":
			return true
		case "datetime":
			return true
		default:
			return false
		}
	default:
		return false
	}
}
