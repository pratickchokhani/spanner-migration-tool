import { provideHttpClient, withInterceptorsFromDi } from '@angular/common/http';
import { TestBed } from '@angular/core/testing';
import { MatSnackBarModule } from '@angular/material/snack-bar';
import IConv, { IInterleavedParent } from '../../model/conv'

import { ConversionService } from './conversion.service';
import ISchemaObjectNode from 'src/app/model/schema-object-node';
import { ObjectExplorerNodeType, Dialect, SourceDbNames } from 'src/app/app.constants';
import ICcTabData from 'src/app/model/cc-tab-data';
import IColumnTabData from 'src/app/model/edit-table'

describe('ConversionService', () => {
  let service: ConversionService;

  beforeEach(() => {
    TestBed.configureTestingModule({
    imports: [],
    providers: [provideHttpClient(withInterceptorsFromDi())]
});
    service = TestBed.inject(ConversionService);
  });

  it('should be created', () => {
    expect(service).toBeTruthy();
  });

  it('getSpannerSequenceNameFromId', () => {
    let conv: IConv = {} as IConv
    conv.SpSequences ={
      "s1": {
        Name: "Sequence1",
        Id: "s1",
        SequenceKind: "BIT REVERSED POSITIVE"
      },
      "s2": {
        Name: "Sequence2",
        Id: "s2",
        SequenceKind: "BIT REVERSED POSITIVE"
      },
    }
    const id = service.getSpannerSequenceNameFromId("s1", conv)
    expect(id).toEqual("Sequence1");
  });

  it('sortNodeChildren', () => {
    const childrenNodes: ISchemaObjectNode[] = [
      {
        name: "tableA",
        type: ObjectExplorerNodeType.Table,
        pos: -1,
        isSpannerNode: false,
        id: "a",
        parent: "",
        parentId: ""
      },
      {
        name: "tableC",
        type: ObjectExplorerNodeType.Table,
        pos: -1,
        isSpannerNode: false,
        id: "c",
        parent: "",
        parentId: ""
      },
      {
        name: "tableB",
        type: ObjectExplorerNodeType.Table,
        pos: -1,
        isSpannerNode: false,
        id: "b",
        parent: "",
        parentId: ""
      }
    ];
    const node : ISchemaObjectNode = {
      name: `Tables`,
      type: ObjectExplorerNodeType.Tables,
      parent: '',
      pos: -1,
      isSpannerNode: true,
      id: '',
      parentId: '',
      children: childrenNodes
    }
    service.sortNodeChildren(node, 'asc')
    expect(node.children![0].name).toEqual("tableA");
    expect(node.children![2].name).toEqual("tableC");
    service.sortNodeChildren(node, 'desc')
    expect(node.children![0].name).toEqual("tableC");
    expect(node.children![2].name).toEqual("tableA");
  });

  it('getSequenceMapping', () => {
    let conv: IConv = {} as IConv
    conv.SpSequences ={
      "s1": {
        Name: "Sequence1",
        Id: "s1",
        SequenceKind: "BIT REVERSED POSITIVE"
      },
      "s2": {
        Name: "Sequence2",
        Id: "s2",
        SequenceKind: "BIT REVERSED POSITIVE"
      },
    }
    const seq = service.getSequenceMapping("s1", conv)
    expect(seq).toEqual({
      spSeqName: "Sequence1",
      spSequenceKind: "BIT REVERSED POSITIVE",
      spSkipRangeMax: undefined,
      spSkipRangeMin: undefined,
      spStartWithCounter: undefined,
    });
  });

  it('getCheckConstraints when src has more data then spanner', () => {
    let conv: IConv = {} as IConv
    conv.SrcSchema = {
      t1: {
        Name: 'test',
        Id: '1',
        Schema: '',
        ColIds: [],
        ColDefs: {},
        PrimaryKeys: [],
        ForeignKeys: [],

        Indexes: [],
        CheckConstraints: [
          { Id: '1', Name: 'Name1', Expr: 'Expr1',ExprId:'Expr1' },
          { Id: '2', Name: 'Name2', Expr: 'Expr2',ExprId:'Expr2' },
        ],
      },
    }
    conv.SpSchema = {
      t1: {
        Name: 'test',
        Id: '1',
        ColIds: [],
        ShardIdColumn: '',
        ColDefs: {},
        PrimaryKeys: [],
        ForeignKeys: [],

        Indexes: [],
        CheckConstraints: [{ Id: '1', Name: 'Name1', Expr: 'Expr1',ExprId:'Expr1' }],
        ParentTable: {} as IInterleavedParent,
        Comment: '',
      },
    }

    const expected: ICcTabData[] = [
      {
        srcSno: '1',
        srcConstraintName: 'Name1',
        srcCondition: 'Expr1',
        spSno: '1',
        spConstraintName: 'Name1',
        spConstraintCondition: 'Expr1',
        spExprId:'Expr1',
        deleteIndex: 'cc1',
      },
      {
        srcSno: '2',
        srcConstraintName: 'Name2',
        srcCondition: 'Expr2',
        spSno: '',
        spConstraintName: '',
        spConstraintCondition: '',
        spExprId:'Expr2',
        deleteIndex: 'cc2',
      },
    ]

    const result = service.getCheckConstraints('t1', conv)
    expect(result).toEqual(expected)
  })

  it('getCheckConstraints when spanner is empty', () => {
    let conv: IConv = {} as IConv
    conv.SrcSchema = {
      t1: {
        Name: 'test',
        Id: '1',
        Schema: '',
        ColIds: [],
        ColDefs: {},
        PrimaryKeys: [],
        ForeignKeys: [],

        Indexes: [],
        CheckConstraints: [
          { Id: '1', Name: 'Name1', Expr: '(col > 0)',ExprId:'Expr1' },
          { Id: '2', Name: 'Name2', Expr: '(col > 0)',ExprId:'Expr2' },
        ],
      },
    }
    conv.SpSchema = {
      t1: {
        Name: 'test',
        Id: '1',
        ColIds: [],
        ShardIdColumn: '',
        ColDefs: {},
        PrimaryKeys: [],
        ForeignKeys: [],

        Indexes: [],
        CheckConstraints: [],
        ParentTable: {} as IInterleavedParent,
        Comment: '',
      },
    }

    const expected: ICcTabData[] = [
      {
        srcSno: '1',
        srcConstraintName: 'Name1',
        srcCondition: '(col > 0)',
        spSno: '',
        spConstraintName: '',
        spConstraintCondition: '',
        spExprId:'Expr1',
        deleteIndex: 'cc1',
      },
      {
        srcSno: '2',
        srcConstraintName: 'Name2',
        srcCondition: '(col > 0)',
        spSno: '',
        spConstraintName: '',
        spConstraintCondition: '',
        spExprId:'Expr2',
        deleteIndex: 'cc2',
      },
    ];

    const result = service.getCheckConstraints('t1', conv)
    expect(result).toEqual(expected)
  })

  it('getCheckConstraints when src is less than spanner', () => {
    let conv: IConv = {} as IConv
    conv.SrcSchema = {
      t1: {
        Name: 'test',
        Id: '1',
        Schema: '',
        ColIds: [],
        ColDefs: {},
        PrimaryKeys: [],
        ForeignKeys: [],

        Indexes: [],
        CheckConstraints: [{ Id: '1', Name: 'Name1', Expr: '(col > 0)', ExprId:'Expr1' }],
      },
    }
    conv.SpSchema = {
      t1: {
        Name: 'test',
        Id: '1',
        ColIds: [],
        ShardIdColumn: '',
        ColDefs: {},
        PrimaryKeys: [],
        ForeignKeys: [],

        Indexes: [],
        CheckConstraints: [
          { Id: '1', Name: 'Name1', Expr: '(col > 0)', ExprId:'Expr1' },
          { Id: '2', Name: 'Name2', Expr: '(col > 0)',ExprId:'Expr2' },
        ],
        ParentTable: {} as IInterleavedParent,
        Comment: '',
      },
    }

    const expected: ICcTabData[] = [
      {
        srcSno: '1',
        srcConstraintName: 'Name1',
        srcCondition: '(col > 0)',
        spSno: '1',
        spConstraintName: 'Name1',
        spConstraintCondition: '(col > 0)',
        spExprId:'Expr1',
        deleteIndex: 'cc1',
      },
      {
        srcSno: '',
        srcConstraintName: '',
        srcCondition: '',
        spSno: '2',
        spConstraintName: 'Name2',
        spConstraintCondition: '(col > 0)',
        spExprId:'Expr2',
        deleteIndex: 'cc2',
      },
    ]

    const result = service.getCheckConstraints('t1', conv)
    expect(result).toEqual(expected)
  })

  it('getColumnMapping for Array Type', () => {
    let conv: IConv = {} as IConv
    conv.SrcSchema = {
      t1: {
        Name: 'test_table',
        Id: 't1',
        Schema: '',
        ColIds: ['c1'],
        ColDefs: {
          c1: {
            Name: 'src_col',
            Id: 'c1',
            Type: { Name: 'text', Mods: [], ArrayBounds: [] },
            NotNull: false,
            Ignored: {Check: false, Identity: false, Default: false, Exclusion: false, ForeignKey: false, AutoIncrement: false},
            DefaultValue: { IsPresent: false, Value: { Statement: '' , ExpressionId: '' } },
            AutoGen: { Name: '', GenerationType: '' },
          },
        },
        PrimaryKeys: [],
        ForeignKeys: [],
        Indexes: [],
        CheckConstraints: [],
      },
    }
    conv.SpSchema = {
      t1: {
        Name: 'test_table',
        Id: 't1',
        ColIds: ['c1'],
        ShardIdColumn: '',
        ColDefs: {
          c1: {
            Name: 'sp_col',
            Id: 'c1',
            T: { Name: 'STRING', IsArray: true, Len: 0 },
            NotNull: false,
            DefaultValue: { IsPresent: false, Value: { Statement: '', ExpressionId: '' } },
            AutoGen: { Name: '', GenerationType: '' },
            Opts: {},
            Comment: '',
          },
        },
        PrimaryKeys: [],
        ForeignKeys: [],
        Indexes: [],
        CheckConstraints: [],
        ParentTable: {} as IInterleavedParent,
        Comment: '',
      },
    }
    conv.DatabaseType = SourceDbNames.Cassandra
    conv.SpDialect = Dialect.GoogleStandardSQLDialect

    const result = service.getColumnMapping('t1', conv);
    const expected: IColumnTabData[] = [
      {
        spOrder: 1,
        srcOrder: 1,
        spColName: 'sp_col',
        spDataType: 'ARRAY<STRING>',
        srcColName: 'src_col',
        srcDataType: 'text',
        spIsPk: false,
        srcIsPk: false,
        spIsNotNull: false,
        srcIsNotNull: false,
        srcId: 'c1',
        srcDefaultValue: '',
        spId: 'c1',
        spColMaxLength: '',
        srcColMaxLength: undefined,
        spAutoGen: { Name: '', GenerationType: '' },
        srcAutoGen: { Name: '', GenerationType: '' },
        spDefaultValue: {
          IsPresent: false,
          Value: {
            ExpressionId: '',
            Statement: '',
          },
        },
        spCassandraOption: '',
      },
    ]
    expect(result).toEqual(expected)
  })

  it('getColumnMapping test for new column', () => {
    let conv: IConv = {} as IConv
    conv.SrcSchema = {
      t1: {
        Name: 'test_table',
        Id: 't1',
        Schema: '',
        ColIds: ['c1'],
        ColDefs: {
          c1: {
            Name: 'src_col',
            Id: 'c1',
            Type: { Name: 'text', Mods: [], ArrayBounds: [] },
            NotNull: false,
            Ignored: {Check: false, Identity: false, Default: false, Exclusion: false, ForeignKey: false, AutoIncrement: false},
            DefaultValue: { IsPresent: false, Value: { Statement: '' , ExpressionId: '' } },
            AutoGen: { Name: '', GenerationType: '' },
          },
        },
        PrimaryKeys: [],
        ForeignKeys: [],
        Indexes: [],
        CheckConstraints: [],
      },
    }
    conv.SpSchema = {
      t1: {
        Name: 'test_table',
        Id: 't1',
        ColIds: ['c1', 'c2'],
        ShardIdColumn: '',
        ColDefs: {
          c1: {
            Name: 'sp_col',
            Id: 'c1',
            T: { Name: 'STRING', IsArray: false, Len: 255 },
            NotNull: false,
            DefaultValue: { IsPresent: false, Value: { Statement: '', ExpressionId: '' } },
            AutoGen: { Name: '', GenerationType: '' },
            Opts: {},
            Comment: '',
          },
          c2: {
            Name: 'new_sp_col',
            Id: 'c2',
            T: { Name: 'INT64', IsArray: false, Len: 0 },
            NotNull: true,
            DefaultValue: { IsPresent: false, Value: { Statement: '', ExpressionId: '' } },
            AutoGen: { Name: '', GenerationType: '' },
            Opts: {},
            Comment: '',
          },
        },
        PrimaryKeys: [],
        ForeignKeys: [],
        Indexes: [],
        CheckConstraints: [],
        ParentTable: {} as IInterleavedParent,
        Comment: '',
      },
    }
    conv.DatabaseType = SourceDbNames.MySQL
    conv.SpDialect = Dialect.GoogleStandardSQLDialect

    const result = service.getColumnMapping('t1', conv);
    const expected: IColumnTabData[] = [
      { spOrder: 1, srcOrder: 1, spColName: 'sp_col', spDataType: 'STRING', srcColName: 'src_col', srcDataType: 'text', spIsPk: false, srcIsPk: false, spIsNotNull: false, srcIsNotNull: false, srcId: 'c1', srcDefaultValue: '', spId: 'c1', spColMaxLength: 255, srcColMaxLength: undefined, spAutoGen: { Name: '', GenerationType: '' }, srcAutoGen: { Name: '', GenerationType: '' }, spDefaultValue: { IsPresent: false, Value: { ExpressionId: '', Statement: '' } }, spCassandraOption: '' },
      { spOrder: 2, srcOrder: '', spColName: 'new_sp_col', spDataType: 'INT64', srcColName: '', srcDataType: '', spIsPk: false, srcIsPk: false, spIsNotNull: true, srcIsNotNull: false, srcId: '', srcDefaultValue: '', spId: 'c2', spColMaxLength: 0, srcColMaxLength: '', spAutoGen: { Name: '', GenerationType: '' }, srcAutoGen: { Name: '', GenerationType: '' }, spDefaultValue: { IsPresent: false, Value: { ExpressionId: '', Statement: '' } }, spCassandraOption: '' },
    ]
    expect(result).toEqual(expected)
  })
});
