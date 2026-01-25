# @metazla/meta-editor

User-friendly metadata editor for MetaMesh. Search and edit file metadata directly through a web interface.

## Features

- **Search**: Find files by title, filename, or hash ID
- **Edit**: Modify all metadata fields including title, season, episode, year, Jellyfin metadata, and more
- **Batch Operations**: Update multiple files simultaneously
- **Real-time**: Direct integration with etcd via REST API
- **User-friendly**: Clean, intuitive interface with form validation

## Development

### Prerequisites

- Node.js 21.6.2+
- pnpm 10.19.0+
- Meta-mesh API server running (provides the `/api/metadata/*` endpoints)

### Running Locally

```bash
# From the root directory
pnpm run start:editor

# Or from the package directory
cd packages/meta-editor
pnpm run dev
```

The editor will be available at `http://localhost:5173` with API proxying to `http://localhost:3000`.

### Building

```bash
# From the root directory
pnpm run build:editor

# Or from the package directory
cd packages/meta-editor
pnpm run build
```

The built files will be in `dist/` directory.

## Docker Deployment

In the Docker development environment, the editor is automatically available at:

```
http://localhost/editor
```

The nginx configuration serves the built files from `/app/packages/meta-editor/dist/`.

## API Integration

The editor communicates with the UnifiedAPIServer (meta-mesh) via REST API:

- `POST /api/metadata/search` - Search for files
- `GET /api/metadata/:hashId` - Get file metadata
- `PUT /api/metadata/:hashId` - Update complete metadata
- `PUT /api/metadata/:hashId/property` - Update specific property
- `POST /api/metadata/batch` - Batch update multiple files

See `src/api/metadataApi.ts` for the API client implementation.

## Usage

### Searching for Files

1. Select search type: "Search by Title/Filename" or "Search by Hash ID"
2. Enter your search query
3. Click "Search"
4. Results will appear in the left sidebar

### Editing Metadata

1. Select a file from the search results
2. Modify any fields in the editor form
3. Click "Save Changes" to persist updates
4. Use "Reset" to discard changes

### Supported Metadata Fields

**Basic Information:**
- Title, Original Title, Show Title
- Video Type (Movie, TV, Anime, Other)
- Year, Season, Episode

**Jellyfin Metadata:**
- Plot, IMDb ID, TMDb ID, AniDB ID
- Director, Studio, Rating, Runtime
- Genres, Premiered Date, Release Date

**File Information:**
- Filename, File Size, MIME Type
- Hash values (SHA-256, MD5, etc.) - read-only

## Project Structure

```
packages/meta-editor/
├── src/
│   ├── api/           # API client for metadata operations
│   ├── components/    # React components
│   │   ├── SearchBar.tsx        # Search interface
│   │   ├── FileList.tsx         # Search results list
│   │   └── MetadataEditor.tsx   # Main editing form
│   ├── types/         # TypeScript type definitions
│   ├── utils/         # Utility functions
│   ├── App.tsx        # Main application component
│   ├── main.tsx       # Entry point
│   └── index.css      # Global styles
├── index.html
├── vite.config.ts
├── tsconfig.json
└── package.json
```

## Technology Stack

- **React 18** - UI framework
- **TypeScript 5.7** - Type safety
- **Vite 6** - Build tool and dev server
- **Native CSS** - Styling (no CSS framework)

## Contributing

When adding new features:

1. Add type definitions to `src/types/index.ts`
2. Update API client in `src/api/metadataApi.ts`
3. Create/update components as needed
4. Update this README with new features

## License

Part of the MetaMesh project.
