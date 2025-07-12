# Object Storage System

A lightweight, file-based object storage system built in Go, providing S3-like functionality with a simple HTTP API and command-line interface.

## Features

- **RESTful API**: HTTP-based API for object storage operations
- **Command-Line Interface**: Easy-to-use CLI for managing buckets and objects
- **File-based Storage**: Simple file system backend for data persistence
- **Metadata Management**: Automatic metadata tracking with ETags and timestamps
- **Content Type Detection**: Automatic content type detection based on file extensions
- **Bucket Operations**: Create and list storage buckets
- **Object Operations**: Upload, download, list, and delete objects
- **Verbose Mode**: Detailed logging for debugging and monitoring

## Architecture

The system consists of two main components:

1. **Storage Server** (`cmd/server/server.go`): HTTP server providing REST API endpoints
2. **CLI Client** (`cmd/cli/client.go`): Command-line tool for interacting with the server

## Installation

### Prerequisites

- Go 1.19 or later
- Make (optional, for using Makefile)

### Build from Source

```bash
# Clone the repository
git clone <repository-url>
cd object-storage-system

# Build all binaries
make build

# Or build individually
make server  # Build server only
make cli     # Build CLI only
```

The binaries will be created in the `build/` directory:
- `build/storage-server` - The storage server
- `build/storage-cli` - The CLI client

## Quick Start

1. **Start the server:**
   ```bash
   make run-server
   # Or directly:
   ./build/storage-server
   ```

2. **Use the CLI in another terminal:**
   ```bash
   # Create a bucket
   ./build/storage-cli mb my-bucket

   # Upload a file
   echo "Hello World" > test.txt
   ./build/storage-cli cp test.txt my-bucket/test.txt

   # List objects
   ./build/storage-cli ls my-bucket

   # Download a file
   ./build/storage-cli cp my-bucket/test.txt downloaded.txt

   # View file content
   ./build/storage-cli cat my-bucket/test.txt
   ```

3. **Run the demo:**
   ```bash
   make demo
   ```

## API Reference

### Server Endpoints

| Method | Endpoint | Description |
|--------|----------|-------------|
| `PUT` | `/buckets/{name}` | Create a new bucket |
| `GET` | `/buckets` | List all buckets |
| `PUT` | `/objects/{bucket}/{key}` | Upload an object |
| `GET` | `/objects/{bucket}/{key}` | Download an object |
| `GET` | `/objects/{bucket}` | List objects in bucket |
| `DELETE` | `/objects/{bucket}/{key}` | Delete an object |
| `HEAD` | `/objects/{bucket}/{key}` | Get object metadata |
| `GET` | `/health` | Health check |

### Server Configuration

The server runs on port 8080 by default and stores data in the `./storage` directory.

## CLI Reference

### Commands

| Command | Description | Example |
|---------|-------------|---------|
| `mb, makebucket` | Create a new bucket | `storage-cli mb my-bucket` |
| `ls, list` | List buckets or objects | `storage-cli ls` or `storage-cli ls my-bucket` |
| `cp, copy` | Upload or download files | `storage-cli cp file.txt my-bucket/file.txt` |
| `rm, remove` | Delete an object | `storage-cli rm my-bucket/file.txt` |
| `cat` | Display object content | `storage-cli cat my-bucket/file.txt` |
| `stat` | Show object information | `storage-cli stat my-bucket/file.txt` |
| `version` | Show version information | `storage-cli version` |
| `help` | Show help message | `storage-cli help` |

### CLI Options

| Option | Description |
|--------|-------------|
| `--server URL` | Storage server URL (default: http://localhost:8080) |
| `--verbose, -v` | Enable verbose output |
| `--help, -h` | Show help message |

### Examples

```bash
# Create a bucket
storage-cli mb photos

# Upload a local file
storage-cli cp vacation.jpg photos/vacation.jpg

# Download a file
storage-cli cp photos/vacation.jpg local-vacation.jpg

# List all buckets
storage-cli ls

# List objects in a bucket
storage-cli ls photos

# Get file information
storage-cli stat photos/vacation.jpg

# View text file content
storage-cli cat documents/readme.txt

# Delete an object
storage-cli rm photos/old-photo.jpg

# Use with different server
storage-cli --server http://remote-server:8080 ls

# Enable verbose output
storage-cli -v cp large-file.zip backups/large-file.zip
```

## Development

### Project Structure

```
.
├── cmd/
│   ├── server/
│   │   └── server.go      # HTTP server implementation
│   └── cli/
│       └── client.go      # CLI client implementation
├── build/                 # Build output directory
├── storage/              # Data storage directory (created at runtime)
│   ├── data/             # Object data files
│   └── metadata/         # Object metadata files
├── Makefile              # Build and development tasks
└── README.md
```

### Available Make Targets

```bash
# Building
make build          # Build all binaries
make server         # Build server only
make cli           # Build CLI only
make clean         # Clean build artifacts

# Running
make run-server    # Start the storage server
make run-cli       # Run CLI client with help
make demo          # Run complete demo

# Development
make init          # Initialize Go module
make deps          # Download dependencies
make fmt           # Format code
make lint          # Lint code (requires golangci-lint)
make test          # Run tests
make test-coverage # Run tests with coverage

# Installation
make install       # Install binaries to GOPATH/bin

# Help
make help          # Show all available targets
make quick-start   # Show quick start guide
```

### Development Setup

1. **Initialize the project:**
   ```bash
   make init
   make deps
   ```

2. **Format and lint code:**
   ```bash
   make fmt
   make lint  # Requires golangci-lint
   ```

3. **Run tests:**
   ```bash
   make test
   make test-coverage
   ```

## Storage Format

The system uses a simple file-based storage format:

- **Data files**: Stored in `storage/data/{bucket}/{object-key}`
- **Metadata files**: Stored in `storage/metadata/{bucket}/{object-key}.json`
- **Bucket metadata**: Stored in `storage/metadata/{bucket-name}.json`

### Metadata Structure

**Object Metadata:**
```json
{
  "key": "file.txt",
  "size": 1024,
  "content_type": "text/plain",
  "etag": "md5-hash",
  "last_modified": "2025-01-02T15:04:05Z"
}
```

**Bucket Metadata:**
```json
{
  "name": "my-bucket",
  "created": "2025-01-02T15:04:05Z"
}
```

## Supported Content Types

The system automatically detects content types based on file extensions:

- Text: `.txt`, `.md`, `.html`, `.css`, `.js`, `.json`, `.xml`
- Images: `.jpg`, `.jpeg`, `.png`, `.gif`, `.svg`
- Documents: `.pdf`
- Archives: `.zip`, `.tar`, `.gz`
- Default: `application/octet-stream`

## Error Handling

The system provides detailed error messages for common scenarios:

- **404 Not Found**: Object or bucket doesn't exist
- **400 Bad Request**: Invalid request format or missing parameters
- **500 Internal Server Error**: Server-side errors with detailed messages
- **405 Method Not Allowed**: Unsupported HTTP method for endpoint

## Limitations

- Single server instance (no clustering)
- No authentication or authorization
- No encryption at rest
- No compression
- Limited to file system storage backend
- No versioning support
- No multipart upload support

## Contributing

1. Fork the repository
2. Create a feature branch
3. Make your changes
4. Add tests for new functionality
5. Run `make fmt` and `make lint`
6. Submit a pull request

## License

This project is licensed under the MIT License - see the LICENSE file for details.

## Troubleshooting

### Common Issues

1. **Server won't start**: Check if port 8080 is available
2. **Permission denied**: Ensure the storage directory is writable
3. **Connection refused**: Verify the server is running and accessible
4. **File not found**: Check bucket and object names for typos

### Debug Mode

Enable verbose mode for detailed logging:

```bash
# CLI verbose mode
storage-cli -v cp file.txt my-bucket/file.txt

# Server logs are written to stdout
```

### Storage Directory

The server creates a `storage` directory in the current working directory. To use a different location, modify the server code or use a symbolic link:

```bash
ln -s /path/to/storage ./storage
```
