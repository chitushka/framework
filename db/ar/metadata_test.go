package ar

import "testing"

type EmbeddedFields struct {
	ID int64 `db:"id,primary"`
}

type embeddedModel struct {
	EmbeddedFields
	HTTPURL string
}

type badOptionModel struct {
	ID int64 `db:"id,unknown"`
}

type duplicateColumnModel struct {
	First  string `db:"value"`
	Second string `db:"value"`
}

type multipleAutoModel struct {
	First  int64 `db:"first,auto"`
	Second int64 `db:"second,auto"`
}

type badVersionModel struct {
	Version string `db:"version,version"`
}

func TestMetadataValidationAndEmbeddedFields(t *testing.T) {
	metadata, err := metadataFor[embeddedModel]()
	if err != nil {
		t.Fatalf("metadataFor() error = %v", err)
	}
	if len(metadata.fields) != 2 || metadata.fields[1].name != "httpurl" || len(metadata.primary) != 1 {
		t.Fatalf("metadata = %#v", metadata)
	}
	if _, err := metadataFor[*embeddedModel](); err == nil {
		t.Fatal("pointer model error = nil")
	}
	if _, err := metadataFor[int](); err == nil {
		t.Fatal("scalar model error = nil")
	}
	if _, err := metadataFor[struct{}](); err == nil {
		t.Fatal("empty model error = nil")
	}
	if _, err := metadataFor[badOptionModel](); err == nil {
		t.Fatal("unknown option error = nil")
	}
	if _, err := metadataFor[duplicateColumnModel](); err == nil {
		t.Fatal("duplicate column error = nil")
	}
	if _, err := metadataFor[multipleAutoModel](); err == nil {
		t.Fatal("multiple auto error = nil")
	}
	if _, err := metadataFor[badVersionModel](); err == nil {
		t.Fatal("bad version error = nil")
	}
}
