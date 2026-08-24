package snapshot

// hashFile streams path through SHA-256 and a newline counter. The bytes are
// never retained, copied, or logged --- the hasher is their only consumer.
func hashFile(path string) (contentHash string, lines int, err error) {
	// TODO: open, io.Copy into a multiwriter of sha256 + line counter.
	return "", 0, errNotImplemented
}

// hashPath returns SHA-256 of a project-relative path, so the wire payload
// carries the shape of the tree without its names.
func hashPath(rel string) string {
	// TODO: sha256 of the slash-normalized relative path.
	return ""
}
