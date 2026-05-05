# Media

`Media` are image and document attachments on a Product. Upload is multipart; reads are simple. Server runs uploads through `ProductAttachment` ingestion and Paperclip variants (generated async after upload).

## Endpoints

| Command | Method + path | Ability | Dry-run |
|---------|---------------|---------|---------|
| `inventory media list <product-id>` | GET /products/{id}/media | `cli:read` | n/a |
| `inventory media show <id>` | GET /media/{id} | `cli:read` | n/a |
| `inventory media upload <product-id>` | POST /products/{id}/media | `cli:write` | none (multipart) |
| `inventory media delete <id>` | DELETE /media/{id} | `cli:write` | none |

## Worked examples

### 1. List media on a product

```bash
ceebee inventory media list prod_42
```

Returns `{id, product_id, filename, mime_type, size, position, alt_text, url, updated_at}`.

### 2. Upload an image with alt text + position

```bash
ceebee inventory media upload prod_42 \
  --file ./hero.jpg \
  --alt-text "Sunset Snorkeling — Divers entering the water at golden hour" \
  --position 1
```

Default `--format json` returns the new Media row. Variants (thumbnails, web sizes) are generated async server-side; the URL returned is the original.

### 3. Upload a PDF brochure

```bash
ceebee inventory media upload prod_42 \
  --file ./brochure.pdf \
  --alt-text "2026 Season Brochure"
```

Accepted MIME types: `image/jpeg`, `image/png`, `image/webp`, `image/gif`, `application/pdf`. Max 10 MiB by default (server may lower per tenant plan).

### 4. Reorder by patching position via re-upload

There is no `media update` endpoint in V1. To reorder, delete and re-upload with a new `--position`. (Phase 2 may add `PATCH /media/{id}`.)

### 5. Delete an outdated media

```bash
ceebee inventory media delete media_88
```

Hard delete; no restore. The remote storage object is reaped async.

## Pitfalls

- ⚠️ **No `update` endpoint in V1.** Want to change `alt_text` or `position`? Delete and re-upload. Phase 2 will add PATCH.
- ⚠️ **Size limits return 413, MIME mismatches return 415.** Both have stable `code` fields in the error envelope. The CLI surfaces these as exit code 12 (validation). Re-encode large JPEGs before upload, or check tenant plan limits via the admin UI.
- ⚠️ **Variants are async.** A successful 201 does NOT mean thumbnails are ready. Customer-facing widgets may show the original until Paperclip finishes — typically seconds, but no in-band signal.
- ⚠️ **No dry-run.** Upload is multipart; the server has no preview path. To check size + MIME locally before upload, use `file ./hero.jpg` and `du -h ./hero.jpg`.

## See also

- [products.md](products.md) — Media attaches to Products only (not options or extras).
