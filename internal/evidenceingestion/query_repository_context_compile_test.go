package evidenceingestion

import (
	"bytes"
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func TestCompileGroundedEvidenceRepositoryDeclarationUsesSmallestDeclarationSpec(t *testing.T) {
	source := []byte(`package sample

type (
	Alpha struct{ Value string }
	Beta  struct{ Other int }
)
`)
	record, files := repositoryDeclarationContextFixture(
		t,
		source,
		"Alpha",
		"type",
	)

	first, err := compileGroundedEvidenceRepositoryContext(record, files)
	if err != nil {
		t.Fatalf("compileGroundedEvidenceRepositoryContext() error = %v", err)
	}
	second, err := compileGroundedEvidenceRepositoryContext(record, files)
	if err != nil {
		t.Fatalf("replay compileGroundedEvidenceRepositoryContext() error = %v", err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("repository declaration replay differs:\nfirst  %+v\nsecond %+v", first, second)
	}
	if first.Status != GroundedEvidenceRepositoryContextStatusAvailable ||
		first.RecordKind != GroundedEvidenceRepositoryRecordDeclaration ||
		first.SearchParticipation ||
		!reflect.DeepEqual(first.SearchCore, record.SourceRefs) ||
		first.Declaration == nil ||
		first.Relation != nil ||
		first.Declaration.ContainerKind != "type" ||
		first.Declaration.PackageClause == nil ||
		first.Declaration.PackageClause.ExactText != "package sample" ||
		!strings.Contains(
			first.Declaration.AtomicContainer.ExactText,
			"Alpha struct{ Value string }",
		) ||
		strings.Contains(first.Declaration.AtomicContainer.ExactText, "Beta") {
		t.Fatalf("repository declaration context = %+v", first)
	}
}

func TestCompileGroundedEvidenceRepositoryRelationSeparatesTypedEndpoints(t *testing.T) {
	record, files := repositoryRelationContextFixture(t, false)

	first, err := compileGroundedEvidenceRepositoryContext(record, files)
	if err != nil {
		t.Fatalf("compileGroundedEvidenceRepositoryContext() error = %v", err)
	}
	second, err := compileGroundedEvidenceRepositoryContext(record, files)
	if err != nil {
		t.Fatalf("replay compileGroundedEvidenceRepositoryContext() error = %v", err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("repository relation replay differs:\nfirst  %+v\nsecond %+v", first, second)
	}
	if first.Status != GroundedEvidenceRepositoryContextStatusAvailable ||
		first.RecordKind != GroundedEvidenceRepositoryRecordRelation ||
		first.RelationKind != CodeRelationKindCall ||
		first.SearchParticipation ||
		!reflect.DeepEqual(first.SearchCore, record.SourceRefs) ||
		first.Declaration != nil ||
		first.Relation == nil ||
		len(first.Relation.Endpoints) != 3 {
		t.Fatalf("repository relation context = %+v", first)
	}
	endpoints := first.Relation.Endpoints
	if endpoints[0].Role != GroundedEvidenceRepositoryContextUsage ||
		endpoints[1].Role != GroundedEvidenceRepositoryContextCaller ||
		endpoints[2].Role != GroundedEvidenceRepositoryContextTarget ||
		endpoints[0].Path != "caller.go" ||
		endpoints[0].Anchor.ExactText != "Target" ||
		endpoints[0].AtomicContainer.ExactText != "Target()" ||
		endpoints[1].Path != "caller.go" ||
		!strings.Contains(endpoints[1].AtomicContainer.ExactText, "func Caller()") ||
		endpoints[2].Path != "target.go" ||
		!strings.Contains(endpoints[2].AtomicContainer.ExactText, "func Target()") ||
		strings.Contains(endpoints[1].AtomicContainer.ExactText, "func Target()") ||
		strings.Contains(endpoints[2].AtomicContainer.ExactText, "func Caller()") {
		t.Fatalf("repository relation endpoints = %+v", endpoints)
	}
}

func TestCompileGroundedEvidenceRepositoryDefinitionOmitsCaller(t *testing.T) {
	record, files := repositoryRelationContextFixture(t, false)
	record.CodeRelation.RelationKind = CodeRelationKindDefinition
	record.CodeRelation.Caller = nil
	record.SourceRefs = record.SourceRefs[1:]

	result, err := compileGroundedEvidenceRepositoryContext(record, files)
	if err != nil {
		t.Fatalf("compileGroundedEvidenceRepositoryContext() error = %v", err)
	}
	if result.Status != GroundedEvidenceRepositoryContextStatusAvailable ||
		result.Relation == nil ||
		len(result.Relation.Endpoints) != 2 ||
		result.Relation.Endpoints[0].Role != GroundedEvidenceRepositoryContextUsage ||
		result.Relation.Endpoints[1].Role != GroundedEvidenceRepositoryContextTarget {
		t.Fatalf("definition repository context = %+v", result)
	}
}

func TestCompileGroundedEvidenceRepositoryContextWithholdsOverBudgetPayload(t *testing.T) {
	t.Run("declaration", func(t *testing.T) {
		source := []byte(
			"package sample\n\nfunc Huge() {\n" +
				strings.Repeat(
					"\t_ = 1\n",
					GroundedEvidenceRepositoryDeclarationMaxBytesV1/4,
				) +
				"}\n",
		)
		record, files := repositoryDeclarationContextFixture(
			t,
			source,
			"Huge",
			"function",
		)

		result, err := compileGroundedEvidenceRepositoryContext(record, files)
		if err != nil {
			t.Fatalf("compileGroundedEvidenceRepositoryContext() error = %v", err)
		}
		if result.Status != GroundedEvidenceRepositoryContextStatusOverBudget ||
			result.LimitReason != "declaration_context_bytes" ||
			result.ReferencedBytes <=
				GroundedEvidenceRepositoryDeclarationMaxBytesV1 ||
			result.Declaration != nil ||
			result.Relation != nil {
			t.Fatalf("over-budget declaration context = %+v", result)
		}
	})

	t.Run("relation", func(t *testing.T) {
		record, files := repositoryRelationContextFixture(t, true)

		result, err := compileGroundedEvidenceRepositoryContext(record, files)
		if err != nil {
			t.Fatalf("compileGroundedEvidenceRepositoryContext() error = %v", err)
		}
		if result.Status != GroundedEvidenceRepositoryContextStatusOverBudget ||
			result.LimitReason != "complete_relation_context_budget" ||
			result.ReferencedBytes <=
				GroundedEvidenceRepositoryRelationEndpointMaxBytesV1 ||
			result.Declaration != nil ||
			result.Relation != nil {
			t.Fatalf("over-budget relation context = %+v", result)
		}
	})
}

func TestCompileGroundedEvidenceRepositoryContextRejectsAuthorityDrift(t *testing.T) {
	t.Run("declaration source ref", func(t *testing.T) {
		source := []byte("package sample\n\nfunc Worker() {}\n")
		record, files := repositoryDeclarationContextFixture(
			t,
			source,
			"Worker",
			"function",
		)
		record.SourceRefs[0].QuotedText = "func Other() {}"

		_, err := compileGroundedEvidenceRepositoryContext(record, files)
		if err == nil || !strings.Contains(err.Error(), "source ref differs") {
			t.Fatalf("source-ref drift error = %v", err)
		}
	})

	t.Run("relation target", func(t *testing.T) {
		record, files := repositoryRelationContextFixture(t, false)
		record.CodeRelation.Target.QuotedTextHash = contentHash([]byte("Other"))

		_, err := compileGroundedEvidenceRepositoryContext(record, files)
		if err == nil || !strings.Contains(err.Error(), "anchor differs") {
			t.Fatalf("relation target drift error = %v", err)
		}
	})
}

func TestGroundedEvidenceRepositoryContextFilesFailsClosed(t *testing.T) {
	record, _ := repositoryRelationContextFixture(t, false)
	files, err := groundedEvidenceRepositoryContextFiles(record)
	if err != nil {
		t.Fatalf("groundedEvidenceRepositoryContextFiles() error = %v", err)
	}
	if !reflect.DeepEqual(files, map[string]string{
		"caller.go": "filesnap:caller",
		"target.go": "filesnap:target",
	}) {
		t.Fatalf("repository context files = %+v", files)
	}

	record.SourceRefs[2].RepositorySnapshotID = "repo-snapshot:drifted"
	_, err = groundedEvidenceRepositoryContextFiles(record)
	assertKind(t, err, ErrorOccurrenceConflict)
}

func TestLoadGroundedEvidenceRepositoryContextsCachesRepositoryFile(t *testing.T) {
	source := []byte("package sample\n\nfunc Worker() {}\n")
	record, files := repositoryDeclarationContextFixture(
		t,
		source,
		"Worker",
		"function",
	)
	file := files["sample.go"]
	db := &groundedEvidenceRepositoryFileQueryer{
		snapshot: *record.RepositorySnapshot,
		file:     file,
	}
	replayedRecord := record
	replayedRecord.ProposalOccurrenceID = "occ:worker-replay"

	contexts, err := loadGroundedEvidenceRepositoryContexts(
		context.Background(),
		db,
		[]ProposalSearchResult{
			{Record: record},
			{Record: replayedRecord},
		},
	)
	if err != nil {
		t.Fatalf("loadGroundedEvidenceRepositoryContexts() error = %v", err)
	}
	if len(contexts) != 2 ||
		contexts[0].Status != GroundedEvidenceRepositoryContextStatusAvailable ||
		contexts[1].Status != GroundedEvidenceRepositoryContextStatusAvailable ||
		contexts[0].ProposalOccurrenceID != record.ProposalOccurrenceID ||
		contexts[1].ProposalOccurrenceID != replayedRecord.ProposalOccurrenceID ||
		db.preflightQueries != 1 ||
		db.contentQueries != 1 {
		t.Fatalf(
			"repository contexts = %+v, queries = preflight %d/content %d",
			contexts,
			db.preflightQueries,
			db.contentQueries,
		)
	}
}

func TestGroundedEvidenceRepositoryContextCompilerCachesParsedFileAcrossRecords(t *testing.T) {
	source := []byte("package sample\n\nfunc Worker() {}\n")
	record, files := repositoryDeclarationContextFixture(
		t,
		source,
		"Worker",
		"function",
	)
	replayedRecord := record
	replayedRecord.ProposalOccurrenceID = "occ:worker-replay"
	compiler := newGroundedEvidenceRepositoryContextCompiler()

	first, err := compiler.compile(record, files)
	if err != nil {
		t.Fatalf("first compiler.compile() error = %v", err)
	}
	second, err := compiler.compile(replayedRecord, files)
	if err != nil {
		t.Fatalf("second compiler.compile() error = %v", err)
	}
	if len(compiler.parsedFiles) != 1 ||
		first.Status != GroundedEvidenceRepositoryContextStatusAvailable ||
		second.Status != GroundedEvidenceRepositoryContextStatusAvailable ||
		first.ProposalOccurrenceID != record.ProposalOccurrenceID ||
		second.ProposalOccurrenceID != replayedRecord.ProposalOccurrenceID {
		t.Fatalf(
			"parsed cache/context = %d/%+v/%+v",
			len(compiler.parsedFiles),
			first,
			second,
		)
	}
}

func TestGroundedEvidenceRepositoryContextCompilerRejectsCachedFileDrift(t *testing.T) {
	source := []byte("package sample\n\nfunc Worker() {}\n")
	_, files := repositoryDeclarationContextFixture(
		t,
		source,
		"Worker",
		"function",
	)
	file := files["sample.go"]
	compiler := newGroundedEvidenceRepositoryContextCompiler()
	if _, err := compiler.parseFile(file); err != nil {
		t.Fatalf("first compiler.parseFile() error = %v", err)
	}
	drifted := file
	drifted.Content = append([]byte(nil), file.Content...)
	drifted.Content[len(drifted.Content)-2] = ' '

	_, err := compiler.parseFile(drifted)
	if err == nil ||
		!strings.Contains(err.Error(), "parsed file identity differs") {
		t.Fatalf("cached file drift error = %v", err)
	}
}

func TestLoadBoundedGroundedEvidenceRepositoryFileWithholdsOversizedContent(t *testing.T) {
	source := []byte("package sample\n\nfunc Worker() {}\n")
	record, files := repositoryDeclarationContextFixture(
		t,
		source,
		"Worker",
		"function",
	)
	file := files["sample.go"]
	file.FileSnapshot.ByteLength =
		int(GroundedEvidenceRepositoryFileMaxBytesV1 + 1)
	db := &groundedEvidenceRepositoryFileQueryer{
		snapshot:       *record.RepositorySnapshot,
		file:           file,
		persistedBytes: GroundedEvidenceRepositoryFileMaxBytesV1 + 1,
	}

	result, err := loadBoundedGroundedEvidenceRepositoryFile(
		context.Background(),
		db,
		record.RepositorySnapshot.ID,
		file.FileSnapshot.ID,
	)
	if err != nil {
		t.Fatalf("loadBoundedGroundedEvidenceRepositoryFile() error = %v", err)
	}
	if result.status != groundedEvidenceRepositoryFileOverBudget ||
		result.persistedBytes !=
			GroundedEvidenceRepositoryFileMaxBytesV1+1 ||
		result.file != nil ||
		db.preflightQueries != 1 ||
		db.contentQueries != 0 {
		t.Fatalf(
			"over-budget repository file = %+v, queries = preflight %d/content %d",
			result,
			db.preflightQueries,
			db.contentQueries,
		)
	}
}

type groundedEvidenceRepositoryFileQueryer struct {
	snapshot         RepositorySnapshot
	file             RepositoryExtractorFile
	persistedBytes   int64
	preflightQueries int
	contentQueries   int
}

func (db *groundedEvidenceRepositoryFileQueryer) query(
	context.Context,
	string,
	...any,
) (sqlRows, error) {
	return nil, fmt.Errorf("unexpected repository context rows query")
}

func (db *groundedEvidenceRepositoryFileQueryer) queryRow(
	_ context.Context,
	query string,
	_ ...any,
) sqlRow {
	if strings.Contains(query, "octet_length(sb.raw_content)") {
		db.preflightQueries++
		persistedBytes := db.persistedBytes
		if persistedBytes == 0 {
			persistedBytes = int64(len(db.file.Content))
		}
		meta := db.file.FileSnapshot
		return groundedEvidenceRepositoryRow{values: []any{
			db.snapshot.ID,
			db.snapshot.RepoID,
			db.snapshot.CommitSHA,
			db.snapshot.ManifestHash,
			db.snapshot.ManifestEntryCount,
			db.snapshot.RevisionVerificationMethod,
			db.snapshot.ManifestContract,
			db.snapshot.FileSelectionContract,
			db.snapshot.SelectedFileCount,
			meta.ID,
			meta.RepositorySnapshotID,
			meta.RepoID,
			meta.CommitSHA,
			meta.Path,
			meta.BlobHash,
			meta.GitBlobOID,
			meta.ByteLength,
			persistedBytes,
		}}
	}
	if strings.Contains(query, "SELECT sb.raw_content") {
		db.contentQueries++
		return groundedEvidenceRepositoryRow{values: []any{
			append([]byte(nil), db.file.Content...),
		}}
	}
	return groundedEvidenceRepositoryRow{
		err: fmt.Errorf("unexpected repository context row query"),
	}
}

type groundedEvidenceRepositoryRow struct {
	values []any
	err    error
}

func (row groundedEvidenceRepositoryRow) Scan(dest ...any) error {
	if row.err != nil {
		return row.err
	}
	if len(dest) != len(row.values) {
		return fmt.Errorf(
			"scan destination count = %d, want %d",
			len(dest),
			len(row.values),
		)
	}
	for index, value := range row.values {
		destination := reflect.ValueOf(dest[index])
		if destination.Kind() != reflect.Pointer || destination.IsNil() {
			return fmt.Errorf("scan destination %d is not a pointer", index)
		}
		target := destination.Elem()
		source := reflect.ValueOf(value)
		if !source.IsValid() {
			target.SetZero()
			continue
		}
		if source.Type().AssignableTo(target.Type()) {
			target.Set(source)
			continue
		}
		if source.Type().ConvertibleTo(target.Type()) {
			target.Set(source.Convert(target.Type()))
			continue
		}
		return fmt.Errorf(
			"scan value %d type %s cannot assign to %s",
			index,
			source.Type(),
			target.Type(),
		)
	}
	return nil
}

func repositoryDeclarationContextFixture(
	t testing.TB,
	source []byte,
	symbol string,
	kind string,
) (ProposalQueryResult, map[string]RepositoryExtractorFile) {
	t.Helper()
	start := bytes.LastIndex(source, []byte(symbol))
	if start < 0 {
		t.Fatalf("symbol %q was not found", symbol)
	}
	end := start + len(symbol)
	snapshot := repositoryContextTestSnapshot(
		"repo-snapshot:declaration",
		"repository-context-declaration",
		strings.Repeat("a", 40),
		1,
	)
	file := repositoryContextTestFile(
		snapshot,
		"filesnap:declaration",
		"sample.go",
		source,
	)
	record := ProposalQueryResult{
		ProposalOccurrenceID: "occ:" + strings.ToLower(symbol),
		SourceBindingKind:    ProposalSourceBindingRepositorySnapshot,
		RepositorySnapshot:   &snapshot,
		SourceRefs: []ResolvedSourceRef{
			repositoryContextTestLineRef(
				snapshot,
				file,
				start,
				end,
				"span:S1",
			),
		},
		CodeFact: &ResolvedCodeFact{
			SchemaVersion:   CodeFactSchemaV1,
			FactKind:        CodeFactKindDeclaration,
			RepoID:          snapshot.RepoID,
			CommitSHA:       snapshot.CommitSHA,
			Path:            file.FileSnapshot.Path,
			FileContentHash: file.FileSnapshot.BlobHash,
			SymbolRef:       "symbol:" + strings.ToLower(symbol),
			SymbolKind:      kind,
			QualifiedName:   "sample." + symbol,
			StartByte:       start,
			EndByte:         end,
			QuotedTextHash:  contentHash(source[start:end]),
			QuotedText:      string(source[start:end]),
		},
	}
	return record, map[string]RepositoryExtractorFile{
		file.FileSnapshot.Path: file,
	}
}

func repositoryRelationContextFixture(
	t testing.TB,
	oversizedTarget bool,
) (ProposalQueryResult, map[string]RepositoryExtractorFile) {
	t.Helper()
	callerSource := []byte(`package sample

func Caller() {
	Target()
}
`)
	targetBody := ""
	if oversizedTarget {
		targetBody = strings.Repeat(
			"\t_ = 1\n",
			GroundedEvidenceRepositoryRelationEndpointMaxBytesV1/4,
		)
	}
	targetSource := []byte("package sample\n\nfunc Target() {\n" + targetBody + "}\n")
	snapshot := repositoryContextTestSnapshot(
		"repo-snapshot:relation",
		"repository-context-relation",
		strings.Repeat("c", 40),
		2,
	)
	callerFile := repositoryContextTestFile(
		snapshot,
		"filesnap:caller",
		"caller.go",
		callerSource,
	)
	targetFile := repositoryContextTestFile(
		snapshot,
		"filesnap:target",
		"target.go",
		targetSource,
	)
	callerStart := bytes.Index(callerSource, []byte("Caller"))
	usageStart := bytes.Index(callerSource, []byte("Target"))
	targetStart := bytes.Index(targetSource, []byte("Target"))
	caller := repositoryContextTestDeclarationEndpoint(
		callerFile,
		"function",
		"sample.Caller",
		callerStart,
		callerStart+len("Caller"),
	)
	target := repositoryContextTestDeclarationEndpoint(
		targetFile,
		"function",
		"sample.Target",
		targetStart,
		targetStart+len("Target"),
	)
	usage := ResolvedCodeUsageSite{
		Path:            callerFile.FileSnapshot.Path,
		FileContentHash: callerFile.FileSnapshot.BlobHash,
		StartByte:       usageStart,
		EndByte:         usageStart + len("Target"),
		QuotedTextHash: contentHash(
			callerSource[usageStart : usageStart+len("Target")],
		),
		QuotedText: "Target",
	}
	return ProposalQueryResult{
			ProposalOccurrenceID: "occ:relation",
			SourceBindingKind:    ProposalSourceBindingRepositorySnapshot,
			RepositorySnapshot:   &snapshot,
			SourceRefs: []ResolvedSourceRef{
				repositoryContextTestLineRef(
					snapshot,
					callerFile,
					caller.StartByte,
					caller.EndByte,
					"span:S1",
				),
				repositoryContextTestLineRef(
					snapshot,
					callerFile,
					usage.StartByte,
					usage.EndByte,
					"span:S2",
				),
				repositoryContextTestLineRef(
					snapshot,
					targetFile,
					target.StartByte,
					target.EndByte,
					"span:S3",
				),
			},
			CodeRelation: &ResolvedCodeRelation{
				SchemaVersion: CodeRelationSchemaV2,
				RelationKind:  CodeRelationKindCall,
				RepoID:        snapshot.RepoID,
				CommitSHA:     snapshot.CommitSHA,
				Caller:        &caller,
				Usage:         usage,
				Target:        target,
			},
		}, map[string]RepositoryExtractorFile{
			callerFile.FileSnapshot.Path: callerFile,
			targetFile.FileSnapshot.Path: targetFile,
		}
}

func repositoryContextTestSnapshot(
	id string,
	repoID string,
	commitSHA string,
	selectedFiles int,
) RepositorySnapshot {
	return RepositorySnapshot{
		ID:                         id,
		RepoID:                     repoID,
		CommitSHA:                  commitSHA,
		ManifestHash:               contentHash([]byte(id + ":manifest")),
		ManifestEntryCount:         selectedFiles,
		RevisionVerificationMethod: RepositoryRevisionVerificationGitV1,
		ManifestContract:           RepositoryManifestGitTreeV1,
		FileSelectionContract:      RepositoryFileSelectionTrackedGoV1,
		SelectedFileCount:          selectedFiles,
	}
}

func repositoryContextTestFile(
	snapshot RepositorySnapshot,
	id string,
	path string,
	source []byte,
) RepositoryExtractorFile {
	return RepositoryExtractorFile{
		FileSnapshot: SourceFileSnapshot{
			ID:                   id,
			RepositorySnapshotID: snapshot.ID,
			RepoID:               snapshot.RepoID,
			CommitSHA:            snapshot.CommitSHA,
			Path:                 path,
			BlobHash:             contentHash(source),
			GitBlobOID:           strings.Repeat("d", 40),
			ByteLength:           len(source),
		},
		Content: append([]byte(nil), source...),
	}
}

func repositoryContextTestDeclarationEndpoint(
	file RepositoryExtractorFile,
	kind string,
	qualifiedName string,
	start int,
	end int,
) ResolvedCodeDeclarationEndpoint {
	exact := file.Content[start:end]
	return ResolvedCodeDeclarationEndpoint{
		Path:            file.FileSnapshot.Path,
		FileContentHash: file.FileSnapshot.BlobHash,
		SymbolRef:       "symbol:" + strings.ToLower(qualifiedName),
		SymbolKind:      kind,
		QualifiedName:   qualifiedName,
		StartByte:       start,
		EndByte:         end,
		QuotedTextHash:  contentHash(exact),
		QuotedText:      string(exact),
	}
}

func repositoryContextTestLineRef(
	snapshot RepositorySnapshot,
	file RepositoryExtractorFile,
	start int,
	end int,
	spanID string,
) ResolvedSourceRef {
	lineStart := bytes.LastIndex(file.Content[:start], []byte("\n")) + 1
	lineEndOffset := bytes.Index(file.Content[end:], []byte("\n"))
	lineEnd := len(file.Content)
	if lineEndOffset >= 0 {
		lineEnd = end + lineEndOffset
	}
	exact := file.Content[lineStart:lineEnd]
	return ResolvedSourceRef{
		TargetKind:           "file_snapshot",
		RepositorySnapshotID: snapshot.ID,
		FileSnapshotID:       file.FileSnapshot.ID,
		RepoID:               snapshot.RepoID,
		CommitSHA:            snapshot.CommitSHA,
		Path:                 file.FileSnapshot.Path,
		SpanID:               spanID,
		StartByte:            lineStart,
		EndByte:              lineEnd,
		QuotedTextHash:       contentHash(exact),
		QuotedText:           string(exact),
	}
}
