package rclone

const (
	Version            = "v1.74.4"
	ArchiveName        = "rclone-v1.74.4-windows-amd64.zip"
	ArchiveURL         = "https://downloads.rclone.org/v1.74.4/rclone-v1.74.4-windows-amd64.zip"
	ArchiveSHA256      = "ef097ef9de37a57feb7d9f9c7afb34148ad3c65be8025f1d8f7f521554a701ea"
	ExecutableEntry    = "rclone-v1.74.4-windows-amd64/rclone.exe"
	ExecutableSHA256   = "492648a3867dbc620188a305e05ff3216aecbf4622bf1a6b5b978ed9c939e18c"
	RemoteName         = "bdm-drive"
	DriveOAuthScope    = "drive.file"
	maxArchiveBytes    = int64(64 << 20)
	maxExecutableBytes = int64(128 << 20)
	defaultOutputLimit = 1 << 20
)
