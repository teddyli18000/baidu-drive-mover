package manifest

import "context"

type Directory struct {
	LogicalPath string
}

type File struct {
	SourceID    string
	SourcePath  string
	LogicalPath string
	ParentPath  string
	Name        string
	Size        int64
	MD5         string
}

type Sink interface {
	UpsertManifestPage(ctx context.Context, taskID string, directories []Directory, files []File) error
}

type Stats struct {
	Directories int64
	Files       int64
	Bytes       int64
}
