// Copyright 2020 Google LLC
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

package mysql

import (
	"fmt"
	"reflect"
	"regexp"
	"strconv"
	"strings"

	"github.com/GoogleCloudPlatform/spanner-migration-tool/common/constants"
	"github.com/GoogleCloudPlatform/spanner-migration-tool/internal"
	"github.com/GoogleCloudPlatform/spanner-migration-tool/logger"
	"github.com/GoogleCloudPlatform/spanner-migration-tool/schema"
	"github.com/GoogleCloudPlatform/spanner-migration-tool/sources/common"
	"github.com/pingcap/tidb/parser"
	"github.com/pingcap/tidb/parser/ast"
	"github.com/pingcap/tidb/parser/format"
	"github.com/pingcap/tidb/parser/opcode"
	"github.com/pingcap/tidb/types"
	driver "github.com/pingcap/tidb/types/parser_driver"
)

var valuesRegexp = regexp.MustCompile("\\((.*?)\\)")
var insertRegexp = regexp.MustCompile("INSERT\\sINTO\\s(.*?)\\sVALUES\\s")
var unsupportedRegexp = regexp.MustCompile("function|procedure|trigger")
var dbcollationRegex = regexp.MustCompile("_[_A-Za-z0-9]+('([^']*)')")

// MysqlSpatialDataTypes is an array of all MySQL spatial data types.
var MysqlSpatialDataTypes = []string{"geometrycollection", "multipoint", "multilinestring", "multipolygon", "point", "linestring", "polygon", "geometry"}
var spatialRegexps = func() []*regexp.Regexp {
	l := make([]*regexp.Regexp, len(MysqlSpatialDataTypes))
	for i, spatial := range MysqlSpatialDataTypes {
		l[i] = regexp.MustCompile("(?i)" + " " + spatial)
	}
	return l
}()
var spatialIndexRegex = regexp.MustCompile("(?i)\\sSPATIAL\\s")
var spatialSridRegex = regexp.MustCompile("(?i)\\sSRID\\s\\d*")

// DbDumpImpl MySQL specific implementation for DdlDumpImpl.
type DbDumpImpl struct {
}

// GetToDdl function below implement the common.DbDump interface.
func (ddi DbDumpImpl) GetToDdl() common.ToDdl {
	return ToDdlImpl{}
}

// ProcessDump processes the mysql dump.
func (ddi DbDumpImpl) ProcessDump(conv *internal.Conv, r *internal.Reader) error {
	return processMySQLDump(conv, r)
}

// ProcessMySQLDump reads mysqldump data from r and does schema or data conversion,
// depending on whether conv is configured for schema mode or data mode.
// In schema mode, ProcessMySQLDump incrementally builds a schema (updating conv).
// In data mode, ProcessMySQLDump uses this schema to convert MySQL data
// and writes it to Spanner, using the data sink specified in conv.
func processMySQLDump(conv *internal.Conv, r *internal.Reader) error {
	for {
		startLine := r.LineNumber
		startOffset := r.Offset
		b, stmts, err := readAndParseChunk(conv, r)
		if err != nil {
			return err
		}
		for _, stmt := range stmts {
			isInsert := processStatement(conv, stmt)
			internal.VerbosePrintf("Parsed SQL command at line=%d/fpos=%d: %d stmts (%d lines, %d bytes) Insert Statement=%v\n", startLine, startOffset, 1, r.LineNumber-startLine, len(b), isInsert)
			logger.Log.Debug(fmt.Sprintf("Parsed SQL command at line=%d/fpos=%d: %d stmts (%d lines, %d bytes) Insert Statement=%v\n", startLine, startOffset, 1, r.LineNumber-startLine, len(b), isInsert))
		}
		if r.EOF {
			break
		}
	}
	internal.ResolveForeignKeyIds(conv.SrcSchema)
	return nil
}

// readAndParseChunk parses a chunk of mysqldump data, returning the bytes read,
// the parsed AST (nil if nothing read), error and whether we've hit end-of-file.
// In effect, we proceed through the file, statement by statement. Many
// statements (e.g. DDL statements) are small, but insert statements can
// be large. Fortunately mysqldump limits the size of insert statements
// (default is 24MB, but configurable via --max-allowed-packet), and so
// the chunks of file we read/parse are manageable, even for mysqldump
// files containing tens or hundreds of GB of data.
func readAndParseChunk(conv *internal.Conv, r *internal.Reader) ([]byte, []ast.StmtNode, error) {
	var l [][]byte

	// Regex for ignoring strings of the form /*!50717 SELECT COUNT(*) INTO @rocksdb_has_p_s_session_variables FROM INFORMATION_SCHEMA.TABLES */;
	// These system generated SQL statements are currently not supported by parser and return error.
	// Pingcap Issue : https://github.com/pingcap/parser/issues/1370
	regexExp := regexp.MustCompile(`^(\/\*[!0-9\s]*SELECT[^\n]*INTO[\s]+@[^\n]*\*\/;\n)$`)
	for {
		b := r.ReadLine()
		l = append(l, b)
		// If we see a semicolon or eof, we're likely to have a command, so try to parse it.
		// Note: we could just parse every iteration, but that would mean more attempts at parsing.
		if strings.Contains(string(b), ";") || r.EOF {
			n := 0
			for i := range l {
				n += len(l[i])
			}
			s := make([]byte, n)
			n = 0
			for i := range l {
				n += copy(s[n:], l[i])
			}
			chunk := string(s)
			matchStatus := regexExp.Match([]byte(chunk))
			if matchStatus {
				fmt.Printf("\nParsing skipped for: %s\n", chunk)
				return s, nil, nil
			}
			tree, _, err := parser.New().Parse(chunk, "", "")
			if err == nil {
				return s, tree, nil
			}
			newTree, ok := handleParseError(conv, chunk, err, l)
			if ok {
				return s, newTree, nil
			}
			// Likely causes of failing to parse:
			// a) complex statements with embedded semicolons e.g. 'CREATE FUNCTION'
			// b) a semicolon embedded in a multi-line comment, or
			// c) a semicolon embedded a string constant or column/table name.
			// We deal with this case by reading another line and trying again.
			conv.Stats.Reparsed++
		}
		if r.EOF {
			return nil, nil, fmt.Errorf("Error parsing last %d line(s) of input", len(l))
		}
	}
}

// processStatement extracts schema information from MySQL
// statements, updating Conv with new schema information, and returning
// true if INSERT statement is encountered.
func processStatement(conv *internal.Conv, stmt ast.StmtNode) bool {
	switch s := stmt.(type) {
	case *ast.CreateTableStmt:
		if conv.SchemaMode() {
			processCreateTable(conv, s)
		}
	case *ast.AlterTableStmt:
		if conv.SchemaMode() {
			processAlterTable(conv, s)
		}
	case *ast.SetStmt:
		if conv.SchemaMode() {
			processSetStmt(conv, s)
		}
	case *ast.InsertStmt:
		processInsertStmt(conv, s)
		return true
	case *ast.CreateIndexStmt:
		if conv.SchemaMode() {
			processCreateIndex(conv, s)
		}
	default:
		conv.SkipStatement(NodeType(stmt))
	}
	return false
}

func processCreateIndex(conv *internal.Conv, stmt *ast.CreateIndexStmt) {
	if stmt.Table == nil {
		logStmtError(conv, stmt, fmt.Errorf("cannot process index statement with nil table"))
		return
	}
	tableName, err := getTableName(stmt.Table)
	if err != nil {
		logStmtError(conv, stmt, fmt.Errorf("can't get table name: %w", err))
		return
	}

	if tbl, ok := internal.GetSrcTableByName(conv.SrcSchema, tableName); ok {
		ctable := conv.SrcSchema[tbl.Id]
		ctable.Indexes = append(ctable.Indexes, schema.Index{
			Id:     internal.GenerateIndexesId(),
			Name:   stmt.IndexName,
			Unique: (stmt.KeyType == ast.IndexKeyTypeUnique),
			Keys:   toSchemaKeys(stmt.IndexPartSpecifications, tbl.ColNameIdMap),
		})
		conv.SrcSchema[tbl.Id] = ctable
	} else {
		conv.Unexpected(fmt.Sprintf("Table %s not found while processing index statement", tableName))
		conv.SkipStatement(NodeType(stmt))
	}
}

func processSetStmt(conv *internal.Conv, stmt *ast.SetStmt) {
	if stmt.Variables != nil && len(stmt.Variables) > 0 {
		for _, variable := range stmt.Variables {
			if variable.Name == "TIME_ZONE" {
				value := variable.Value
				switch val := value.(type) {
				case *driver.ValueExpr:
					if val.GetValue() == nil {
						logStmtError(conv, stmt, fmt.Errorf("found nil value in 'SET TIME_ZONE' statement"))
						return
					}
					conv.TimezoneOffset = fmt.Sprintf("%v", val.GetValue())
				default:
					// mysqldump saves the value of TIME_ZONE (in OLD_TIME_ZONE) at
					// the start of the dump, changes TIME_ZONE, dumps table schema
					// and data, and then restores TIME_ZONE using OLD_TIME_ZONE at the
					// end of the dump file. We track the setting of TIME_ZONE, but
					// ignore the restore statements.
					return
				}
			}
		}
	}
}

func processCreateTable(conv *internal.Conv, stmt *ast.CreateTableStmt) {
	if stmt.Table == nil {
		logStmtError(conv, stmt, fmt.Errorf("table is nil"))
		return
	}
	tableId := internal.GenerateTableId()
	tableName, err := getTableName(stmt.Table)
	internal.VerbosePrintf("processing create table elem=%s stmt=%v\n", tableName, stmt)
	logger.Log.Debug(fmt.Sprintf("processing create table elem=%s stmt=%v\n", tableName, stmt))

	if err != nil {
		logStmtError(conv, stmt, fmt.Errorf("can't get table name: %w", err))
		return
	}
	var colIds []string
	colDef := make(map[string]schema.Column)
	colNameIdMap := make(map[string]string)
	var keys []schema.Key
	var fkeys []schema.ForeignKey
	var index []schema.Index

	checkConstraints := getCheckConstraints(stmt.Constraints)

	for _, element := range stmt.Cols {
		_, col, constraint, err := processColumn(conv, tableName, element)
		if err != nil {
			logStmtError(conv, stmt, err)
			return
		}
		col.Id = internal.GenerateColumnId() //assigns new id
		colDef[col.Id] = col
		colIds = append(colIds, col.Id)
		colNameIdMap[col.Name] = col.Id
		if constraint.isPk {
			keys = append(keys, schema.Key{ColId: col.Id})
		}
		if constraint.fk.ColumnNames != nil {
			fkeys = append(fkeys, constraint.fk)
		}
		if constraint.isUniqueKey {
			// Convert unique column constraint in MySQL to a corresponding unique index in Spanner since
			// Spanner doesn't support unique constraints on columns.
			// TODO: Avoid Spanner-specific schema transformations in this file -- they should only
			// appear in toddl.go. This file should focus on generic transformation from source
			// database schemas into schema.go.
			idxId := internal.GenerateIndexesId()
			index = append(index, schema.Index{
				Name:   "",
				Id:     idxId,
				Unique: true,
				Keys: []schema.Key{
					{
						ColId: col.Id,
						Desc:  false,
					},
				},
			})
		}
	}
	conv.SchemaStatement(NodeType(stmt))
	conv.SrcSchema[tableId] = schema.Table{
		Id:               tableId,
		Name:             tableName,
		ColIds:           colIds,
		ColNameIdMap:     colNameIdMap,
		ColDefs:          colDef,
		PrimaryKeys:      keys,
		ForeignKeys:      fkeys,
		Indexes:          index,
		CheckConstraints: checkConstraints,
	}
	for _, constraint := range stmt.Constraints {
		processConstraint(conv, tableId, constraint, "CREATE TABLE", conv.SrcSchema[tableId].ColNameIdMap)
	}
}

func processConstraint(conv *internal.Conv, tableId string, constraint *ast.Constraint, stmtType string, colNameToIdMap map[string]string) {
	st := conv.SrcSchema[tableId]
	switch ct := constraint.Tp; ct {
	case ast.ConstraintPrimaryKey:
		checkEmpty(conv, st.PrimaryKeys, stmtType) // Drop any previous primary keys.
		st.PrimaryKeys = toSchemaKeys(constraint.Keys, colNameToIdMap)
		// In Spanner, primary key columns are usually annotated with NOT NULL,
		// but this can be omitted to allow NULL values in key columns.
		// In MySQL, the primary key constraint is a combination of
		// NOT NULL and UNIQUE i.e. primary keys must be NOT NULL and UNIQUE.
		// We preserve MySQL semantics and enforce NOT NULL and UNIQUE.
		updateCols(conv, ast.ConstraintPrimaryKey, constraint.Keys, st.ColDefs, colNameToIdMap)
	case ast.ConstraintForeignKey:
		st.ForeignKeys = append(st.ForeignKeys, toForeignKeys(conv, constraint))
	case ast.ConstraintIndex:
		idxId := internal.GenerateIndexesId()
		st.Indexes = append(st.Indexes, schema.Index{Name: constraint.Name, Id: idxId, Keys: toSchemaKeys(constraint.Keys, colNameToIdMap)})
	case ast.ConstraintUniq:
		idxId := internal.GenerateIndexesId()
		// Convert unique column constraint in mysql to a corresponding unique index in schema
		// Note that schema represents all unique constraints as indexes.
		st.Indexes = append(st.Indexes, schema.Index{Name: constraint.Name, Id: idxId, Unique: true, Keys: toSchemaKeys(constraint.Keys, colNameToIdMap)})
	default:
		updateCols(conv, ct, constraint.Keys, st.ColDefs, colNameToIdMap)
	}
	conv.SrcSchema[tableId] = st
}

// method to get check constraints using tiDB parser
func getCheckConstraints(constraints []*ast.Constraint) (checkConstraints []schema.CheckConstraint) {
	for _, constraint := range constraints {
		if constraint.Tp == ast.ConstraintCheck {
			exp := expressionToString(constraint.Expr)
			exp = dbcollationRegex.ReplaceAllString(exp, "$1")
			exp = checkAndAddParentheses(exp)
			checkConstraint := schema.CheckConstraint{
				Name:   constraint.Name,
				Expr:   exp,
				ExprId: internal.GenerateExpressionId(),
				Id:     internal.GenerateCheckConstrainstId(),
			}
			checkConstraints = append(checkConstraints, checkConstraint)
		}
	}
	return checkConstraints
}

// converts an AST expression node to its string representation.
func expressionToString(expr ast.Node) string {
	var sb strings.Builder
	restoreCtx := format.NewRestoreCtx(format.RestoreStringSingleQuotes|format.RestoreKeyWordUppercase, &sb)
	if err := expr.Restore(restoreCtx); err != nil {
		fmt.Errorf("Error restoring expression: %v\n", err)
		return ""
	}
	return sb.String()
}

// toSchemaKeys converts a string list of MySQL keys to schema keys.
// Note that we map all MySQL keys to ascending ordered schema keys.
// For primary keys: this is fine because MySQL primary keys are always ascending.
// However, for non-primary keys (aka indexes) this is incorrect: we are dropping
// the MySQL key order specification, as mysqldump parser is not able to parse the
// order. Check this for more details:
// https://github.com/GoogleCloudPlatform/spanner-migration-tool/issues/96
// TODO: Resolve ordering issue for non-primary keys.
func toSchemaKeys(columns []*ast.IndexPartSpecification, colNameToIdMap map[string]string) (keys []schema.Key) {
	for _, spec := range columns {
		specColName := spec.Column.OrigColName()
		if colId, ok := colNameToIdMap[specColName]; ok {
			keys = append(keys, schema.Key{ColId: colId})
		}
	}
	return keys
}

// toForeignKeys converts a MySQL ast foreign key constraint to
// schema foreign keys.
func toForeignKeys(conv *internal.Conv, fk *ast.Constraint) (fkey schema.ForeignKey) {
	columns := fk.Keys
	referTable, err := getTableName(fk.Refer.Table)
	if err != nil {
		conv.Unexpected(err.Error())
		return schema.ForeignKey{}
	}
	referColumns := fk.Refer.IndexPartSpecifications
	var colNames, referColNames []string
	for i, column := range columns {
		colNames = append(colNames, column.Column.Name.String())
		referColNames = append(referColNames, referColumns[i].Column.Name.String())
	}
	onDelete := fk.Refer.OnDelete.ReferOpt.String()
	onUpdate := fk.Refer.OnUpdate.ReferOpt.String()

	if onDelete == "" {
		onDelete = constants.FK_NO_ACTION
	}

	if onUpdate == "" {
		onUpdate = constants.FK_NO_ACTION
	}

	fkey = schema.ForeignKey{
		Id:               internal.GenerateForeignkeyId(),
		Name:             fk.Name,
		ColumnNames:      colNames,
		ReferTableName:   referTable,
		ReferColumnNames: referColNames,
		OnDelete:         onDelete,
		OnUpdate:         onUpdate}
	return fkey
}

func updateCols(conv *internal.Conv, ct ast.ConstraintType, colNames []*ast.IndexPartSpecification, colDef map[string]schema.Column, colNameToIdMap map[string]string) {
	for _, column := range colNames {
		colName := column.Column.OrigColName()
		cid := colNameToIdMap[colName]
		cd := colDef[cid]
		switch ct {
		case ast.ConstraintCheck:
			cd.Ignored.Check = true
		case ast.ConstraintPrimaryKey:
			cd.NotNull = true
		}
		colDef[cid] = cd
	}
}

func processAlterTable(conv *internal.Conv, stmt *ast.AlterTableStmt) {
	if stmt.Table == nil {
		logStmtError(conv, stmt, fmt.Errorf("table is nil"))
		return
	}
	tableName, err := getTableName(stmt.Table)

	if err != nil {
		logStmtError(conv, stmt, fmt.Errorf("can't get table name: %w", err))
		return
	}
	if tbl, ok := internal.GetSrcTableByName(conv.SrcSchema, tableName); ok {
		for _, item := range stmt.Specs {
			switch alterType := item.Tp; alterType {
			case ast.AlterTableAddConstraint:
				processConstraint(conv, tbl.Id, item.Constraint, "ALTER TABLE", tbl.ColNameIdMap)
				conv.SchemaStatement(NodeType(stmt))
			case ast.AlterTableModifyColumn:
				colname, col, constraint, err := processColumn(conv, tableName, item.NewColumns[0])
				if err != nil {
					logStmtError(conv, stmt, err)
					return
				}
				col.Id = tbl.ColNameIdMap[colname]
				conv.SrcSchema[tbl.Id].ColDefs[col.Id] = col
				if constraint.isPk {
					ctable := conv.SrcSchema[tbl.Id]
					checkEmpty(conv, ctable.PrimaryKeys, "ALTER TABLE")
					ctable.PrimaryKeys = []schema.Key{{ColId: col.Id}}
					conv.SrcSchema[tbl.Id] = ctable
				}
				if constraint.fk.ColIds != nil {
					ctable := conv.SrcSchema[tbl.Id]
					ctable.ForeignKeys = append(ctable.ForeignKeys, constraint.fk)
					conv.SrcSchema[tbl.Id] = ctable
				}
				if constraint.isUniqueKey {
					// Convert unique column constraint in mysql to a corresponding unique index in schema
					// Note that schema represents all unique constraints as indexes.
					ctable := conv.SrcSchema[tbl.Id]
					ctable.Indexes = append(ctable.Indexes, schema.Index{Name: "", Unique: true, Keys: []schema.Key{schema.Key{ColId: colname, Desc: false}}})
					conv.SrcSchema[tbl.Id] = ctable
				}
				conv.SchemaStatement(NodeType(stmt))
			default:
				conv.SkipStatement(NodeType(stmt))
			}
		}
	} else {
		conv.SkipStatement(NodeType(stmt))
	}
}

// getTableName extracts the table name from *ast.TableName table, and returns
// the raw extracted name (the MySQL table name).
// *ast.TableName is used to represent table names. It consists of two components:
//
//	Schema: schemas in MySQL db often unspecified;
//	Name: name of the table
//
// We build a table name from these components as follows:
// a) nil components are dropped.
// b) if more than one component is specified, they are joined using "."
//
//	(Note that Spanner doesn't allow "." in table names, so this
//	will eventually get re-mapped when we construct the Spanner table name).
//
// c) return error if Table is nil or "".
func getTableName(table *ast.TableName) (string, error) {
	var l []string

	if table.Schema.String() != "" {
		l = append(l, table.Schema.String())
	}
	if table.Name.String() == "" {
		return "", fmt.Errorf("tablename is empty: can't build table name")
	}
	l = append(l, table.Name.String())
	return strings.Join(l, "."), nil
}

func processColumn(conv *internal.Conv, tableName string, col *ast.ColumnDef) (string, schema.Column, columnConstraint, error) {
	if col.Name == nil {
		return "", schema.Column{}, columnConstraint{}, fmt.Errorf("column name is nil")
	}
	name := col.Name.OrigColName()
	if col.Tp == nil {
		return "", schema.Column{}, columnConstraint{}, fmt.Errorf("can't get column type for %s: %w", name, fmt.Errorf("found nil *ast.ColumnDef.Tp"))
	}
	tid, mods := getTypeModsAndID(conv, col.Tp.String())
	ty := schema.Type{
		Name:        tid,
		Mods:        mods,
		ArrayBounds: getArrayBounds(col.Tp.String(), col.Tp.GetElems())}
	column := schema.Column{Name: name, Type: ty}
	return name, column, updateColsByOption(conv, tableName, col, &column), nil
}

type columnConstraint struct {
	isPk        bool
	isUniqueKey bool
	fk          schema.ForeignKey
}

// updateColsByOption is specifially for ColDef constraints.
// ColumnOption type is used for parsing column constraint info from MySQL.
func updateColsByOption(conv *internal.Conv, tableName string, col *ast.ColumnDef, column *schema.Column) columnConstraint {
	var cc columnConstraint
	for _, elem := range col.Options {
		switch op := elem.Tp; op {
		case ast.ColumnOptionPrimaryKey:
			column.NotNull = true
			// If primary key is defined in a column then `isPk` will be true
			// and this column will be added in colDef as primary keys.
			cc.isPk = true
		case ast.ColumnOptionNotNull:
			column.NotNull = true
		case ast.ColumnOptionAutoIncrement:
			column.Ignored.AutoIncrement = true
		case ast.ColumnOptionDefaultValue:
			// If a data type specification includes no explicit DEFAULT
			// value, MySQL determines if the column can take NULL as a value
			// and the column is defined with DEFAULT NULL clause in mysqldump.
			// This case is ignored from issue reporting of 'Default' value.
			v, ok := elem.Expr.(*driver.ValueExpr)
			nullDefault := ok && v.GetValue() == nil
			if !nullDefault {
				column.Ignored.Default = true
			}
		case ast.ColumnOptionUniqKey:
			cc.isUniqueKey = true
		case ast.ColumnOptionCheck:
			column.Ignored.Check = true
		case ast.ColumnOptionReference:
			column := col.Name.String()
			referTable, err := getTableName(elem.Refer.Table)
			if err != nil {
				conv.Unexpected(err.Error())
				continue
			}
			referColumn := elem.Refer.IndexPartSpecifications[0].Column.Name.String()

			// Note that foreign key constraints that are part of a column definition
			// have no name, so we leave fkey.Name as the empty string.
			fkey := schema.ForeignKey{
				ColumnNames:      []string{column},
				ReferTableName:   referTable,
				ReferColumnNames: []string{referColumn},
				OnDelete:         elem.Refer.OnDelete.ReferOpt.String(),
				OnUpdate:         elem.Refer.OnUpdate.ReferOpt.String()}
			cc.fk = fkey
		}
	}
	return cc
}

// getTypeModsAndID returns ID and mods of column datatype.
func getTypeModsAndID(conv *internal.Conv, columnType string) (string, []int64) {
	// There are no methods in pincap parser to retirieve ID and mods.
	// We will process columnType eg:'varchar(40)' and split ID from the string.
	// We retrieve mods using regex expression and convert it to INT64.
	id := columnType
	var mods []int64
	if strings.Contains(columnType, "(") {
		id = strings.Split(columnType, "(")[0]
		// For 'set' and 'enum' datatypes, values provided are not maxLength.
		if id == "set" || id == "enum" {
			return id, nil
		}
		values := valuesRegexp.FindString(columnType)
		strMods := strings.Split(values[1:len(values)-1], ",")
		for _, i := range strMods {
			j, err := strconv.ParseInt(i, 10, 64)
			if err != nil {
				conv.Unexpected(fmt.Sprintf("Unable to get modifiers for `%s` datatype.", id))
				return id, nil
			}
			mods = append(mods, j)
		}
	}
	// 'BINARY' keyword suffix will be added to all blob datatypes by parser.
	// Eg: mediumblob BINARY. It needs to be trimmed to retrieve ID.
	if strings.Contains(id, " ") {
		id = strings.TrimSuffix(columnType, " BINARY")
	}
	return id, mods
}

// handleParseError handles error while parsing mysqldump
// statements and attempts at creating parsable chunk.
// Error can be due to insert statement, unsupported Spatial
// datatypes in create statement or unsupported stored programs.
func handleParseError(conv *internal.Conv, chunk string, err error, l [][]byte) ([]ast.StmtNode, bool) {
	// Check error for statements that are not supported by Pingcap parser
	// such as delimiter, function, procedures and triggers.
	// If the error is due to a delimiter, we reparse till the chunk
	// contains 2 delimiters, which is the typical way delimiters
	// are used by mysqldump e.g.
	// DELIMITER ;; - First one redefines delimiter to something unusual.
	// - What follows is the definition of a function, procedure
	// - or trigger, which can freely use ';' in its body.
	// DELIMITER ; - Second one restores default delimiter.
	// We also handle the case of functions, procedures or triggers
	// without a delimiter statement.
	errMsg := strings.ToLower(err.Error())
	if unsupportedRegexp.MatchString(errMsg) || strings.Contains(errMsg, "delimiter") {
		if strings.Count(strings.ToLower(chunk), "delimiter") == 1 {
			return nil, false
		}
		return nil, skipUnsupported(conv, strings.ToLower(chunk))
	}
	// Check if error is due to Insert statement.
	insertStmtPrefix := insertRegexp.FindString(chunk)
	if insertStmtPrefix != "" {
		// Sending chunk as list of values and insertStmtPrefix separately
		// to avoid column names being treated as values by valuesRegexp.
		// Eg : INSERT INTO mytable (a, b c) VALUES (1, 2, 3),(4, 5, 6);
		// insertStmtPrefix = INSERT INTO mytable (a, b c) VALUES
		// valuesChunk = (1, 2, 3),(4, 5, 6);
		valuesChunk := insertRegexp.Split(chunk, 2)[1] // stripping off insertStmtPrefix
		return handleInsertStatement(conv, valuesChunk, insertStmtPrefix)
	}
	// Handle error if it is due to spatial datatype as it is not supported by Pingcap parser.
	for _, spatial := range MysqlSpatialDataTypes {
		if strings.Contains(errMsg, `near "`+spatial) {
			if conv.SchemaMode() {
				conv.Unexpected(fmt.Sprintf("Unsupported datatype '%s' encountered while parsing following statement at line number %d : \n%s", spatial, len(l), chunk))
				internal.VerbosePrintf("Converting datatype '%s' to 'Text' and retrying to parse the statement\n", spatial)
				logger.Log.Debug(fmt.Sprintf("Converting datatype '%s' to 'Text' and retrying to parse the statement\n", spatial))

			}
			return handleSpatialDatatype(conv, chunk, l)
		}
	}
	return nil, false
}

// handleInsertStatement handles error in parsing the insert statement.
// Likely causes of failing to parse Insert statement:
//
//	a) Due to some invalid value.
//	b) chunk size is more than what pingcap parser could handle (more than 40MB in size).
//
// We deal with this cases by extracting all rows and creating
// extended insert statements. Then we parse one Insert statement
// at a time, ensuring no size issue and skipping only invalid entries.
func handleInsertStatement(conv *internal.Conv, chunk, insertStmtPrefix string) ([]ast.StmtNode, bool) {
	var stmts []ast.StmtNode
	values := valuesRegexp.FindAllString(chunk, -1)

	if len(values) == 0 {
		return nil, false
	}
	for _, value := range values {
		chunk = insertStmtPrefix + value + ";"
		newTree, _, err := parser.New().Parse(chunk, "", "")
		if err != nil {
			if conv.SchemaMode() {
				conv.Unexpected(fmt.Sprintf("Either unsupported value is encountered or syntax is incorrect for following statement : \n%s", chunk))
			}
			conv.SkipStatement("InsertStmt")
			continue
		}
		stmts = append(stmts, newTree[0])
	}
	return stmts, true
}

// handleSpatialDatatype handles error in parsing spatial datatype.
// We parse chunk again after taking these actions:
// a) Replace spatial datatype with 'text'.
// b) Remove 'SPATIAL' keyword from Index/Key.
// c) Remove SRID(spatial reference identifier) attribute.
func handleSpatialDatatype(conv *internal.Conv, chunk string, l [][]byte) ([]ast.StmtNode, bool) {
	if !conv.SchemaMode() {
		return nil, true
	}
	for _, spatialRegexp := range spatialRegexps {
		chunk = spatialRegexp.ReplaceAllString(chunk, " text")
	}
	chunk = spatialIndexRegex.ReplaceAllString(chunk, "")
	chunk = spatialSridRegex.ReplaceAllString(chunk, "")
	newTree, _, err := parser.New().Parse(chunk, "", "")
	if err != nil {
		return nil, false
	}
	return newTree, true
}

// skipUnsupported skips the stored programs that are not supported
// by pingcap parser.
func skipUnsupported(conv *internal.Conv, chunk string) bool {
	createOrdrop := "Create"
	if strings.Contains(chunk, "drop") {
		createOrdrop = "Drop"
	}
	switch {
	case strings.Contains(chunk, "trigger"):
		conv.SkipStatement(createOrdrop + "TrigStmt")
	case strings.Contains(chunk, "procedure"):
		conv.SkipStatement(createOrdrop + "ProcedureStmt")
	case strings.Contains(chunk, "function"):
		conv.SkipStatement(createOrdrop + "FunctionStmt")
	default:
		return false
	}
	return true
}

// getArrayBounds calculate array bound for only set data type
// and we do not expect multidimensional array.
func getArrayBounds(ft string, elem []string) []int64 {
	if strings.HasPrefix(ft, "set") {
		return []int64{int64(len(elem))}
	}
	return nil
}

func processInsertStmt(conv *internal.Conv, stmt *ast.InsertStmt) {
	if stmt.Table == nil {
		logStmtError(conv, stmt, fmt.Errorf("source table is nil"))
		return
	}
	srcTable, err := getTableNameInsert(stmt.Table)
	if err != nil {
		logStmtError(conv, stmt, fmt.Errorf("can't get source table name: %w", err))
		return
	}
	tableId, _ := internal.GetTableIdFromSrcName(conv.SrcSchema, srcTable)
	if conv.SchemaMode() {
		conv.Stats.Rows[srcTable] += int64(len(stmt.Lists))
		conv.DataStatement(NodeType(stmt))
		return
	}

	srcSchema, ok2 := conv.SrcSchema[tableId]
	if !ok2 {
		conv.Unexpected(fmt.Sprintf("Can't get schemas for table %s", conv.SrcSchema[tableId].Name))
		conv.Stats.BadRows[srcTable] += conv.Stats.Rows[srcTable]
		return
	}
	srcColIds := []string{}
	srcCols, err2 := getCols(stmt)
	if err2 != nil {
		// In MySQL, column names might not be specified in insert statement so instead of
		// throwing error we will try to retrieve columns from source schema.
		for _, srcColId := range conv.SrcSchema[tableId].ColIds {
			srcCols = append(srcCols, conv.SrcSchema[tableId].ColDefs[srcColId].Name)
			srcColIds = append(srcColIds, srcColId)
		}
		if len(srcColIds) == 0 {
			conv.Unexpected(fmt.Sprintf("Can't get columns for table %s", srcTable))
			conv.Stats.BadRows[srcTable] += conv.Stats.Rows[srcTable]
			return
		}
	} else {
		for _, srcColName := range srcCols {
			colId, _ := internal.GetColIdFromSrcName(conv.SrcSchema[tableId].ColDefs, srcColName)
			srcColIds = append(srcColIds, colId)
		}
	}

	var values []string
	if stmt.Lists == nil {
		logStmtError(conv, stmt, fmt.Errorf("can't get column values"))
		return
	}
	commonColIds := common.IntersectionOfTwoStringSlices(conv.SpSchema[tableId].ColIds, srcColIds)
	spSchema := conv.SpSchema[tableId]
	colNameIdMap := internal.GetSrcColNameIdMap(conv.SrcSchema[tableId])
	for _, row := range stmt.Lists {
		values, err = getVals(row)
		//prepare values
		newValues, err2 := common.PrepareValues(conv, tableId, colNameIdMap, commonColIds, srcCols, values)
		if err2 != nil {
			conv.Unexpected(fmt.Sprintf("Error while converting data: %s\n", err))
			conv.StatsAddBadRow(srcSchema.Name, conv.DataMode())
			conv.CollectBadRow(srcSchema.Name, srcCols, values)
			continue
		}
		ProcessDataRow(conv, tableId, commonColIds, srcSchema, spSchema, newValues, internal.AdditionalDataAttributes{ShardId: ""})
	}
}

func getCols(stmt *ast.InsertStmt) ([]string, error) {
	if stmt.Columns == nil {
		return nil, fmt.Errorf("No columns found in insert statement ")
	}
	var colnames []string
	for _, column := range stmt.Columns {
		colnames = append(colnames, column.OrigColName())
	}
	return colnames, nil
}

func getVals(row []ast.ExprNode) ([]string, error) {
	if len(row) == 0 {
		return nil, fmt.Errorf("Found row with zero length")
	}
	var values []string
	for _, item := range row {
		switch valueNode := item.(type) {
		case *driver.ValueExpr:
			values = append(values, fmt.Sprintf("%v", valueNode.GetValue()))
		case *ast.UnaryOperationExpr:
			if valueNode.Op != opcode.Minus {
				return nil, fmt.Errorf("unexpected UnaryOperationExpr node with opcode %v", valueNode.Op)
			}
			valExpr, ok := valueNode.V.(*driver.ValueExpr)
			if !ok {
				return nil, fmt.Errorf("unexpected UnaryOperationExpr node with value type %T", valueNode.V)
			}
			value, err := getNegativeUnaryVals(valExpr)
			if err != nil {
				return nil, fmt.Errorf("unexpected UnaryOperationExpr node with value %v", valExpr.GetValue())
			}
			values = append(values, value)
		default:
			return nil, fmt.Errorf("unexpected value node %T", valueNode)
		}
	}
	return values, nil
}

func getNegativeUnaryVals(valExpr *driver.ValueExpr) (string, error) {
	switch val := valExpr.GetValue().(type) {
	case int64:
		return fmt.Sprintf("%v", -1*val), nil
	case *types.MyDecimal:
		floatVal, err := val.ToFloat64()
		if err != nil {
			return "", fmt.Errorf("unexpected UnaryOperationExpr with value %v", val)
		}
		return fmt.Sprintf("%v", -1*floatVal), nil
	default:
		return "", fmt.Errorf("unexpected UnaryOperationExpr value with type %T", val)
	}
}

func getTableNameInsert(stmt *ast.TableRefsClause) (string, error) {
	if stmt.TableRefs == nil {
		return "", fmt.Errorf("can't build table name as tablerefs is empty")
	}
	if stmt.TableRefs.Left == nil {
		return "", fmt.Errorf("can't build table name as Tablerefs.Left is empty")
	}
	if table, ok := stmt.TableRefs.Left.(*ast.TableSource); ok {
		if tablenode, ok := table.Source.(*ast.TableName); ok {
			return getTableName(tablenode)
		}
		return "", fmt.Errorf("Can't build table name as table source is of different type")
	}
	return "", fmt.Errorf("stmt.TableRefs.Left is different type, can't build table name")
}

func logStmtError(conv *internal.Conv, stmt ast.StmtNode, err error) {
	conv.Unexpected(fmt.Sprintf("Processing %v statement: %s", reflect.TypeOf(stmt), err))
	conv.ErrorInStatement(NodeType(stmt))
}

// checkEmpty verifies that pkeys is empty and generates a warning if it isn't.
// MySQL explicitly forbids multiple primary keys.
func checkEmpty(conv *internal.Conv, pkeys []schema.Key, stmtType string) {
	if len(pkeys) != 0 {
		conv.Unexpected(fmt.Sprintf("Multiple primary keys found. `%s` statement is overwriting primary key", stmtType))
	}
}

// NodeType strips off "ast." prefix from ast.StmtNode type.
func NodeType(n ast.StmtNode) string {
	return strings.TrimPrefix(reflect.TypeOf(n).String(), "*ast.")
}
