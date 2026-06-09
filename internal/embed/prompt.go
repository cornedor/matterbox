package embed

// EmbeddingGemma is prompt-sensitive: it was trained with distinct instruction
// prefixes for the two sides of a retrieval task, and omitting them measurably
// degrades search quality. Stored messages are embedded as DOCUMENTS; the
// search box text is embedded as a QUERY. These helpers centralise that format
// so the indexer and the search path can't drift apart.
//
// Formats are from EmbeddingGemma's model card:
//
//	document: "title: none | text: {text}"
//	query:    "task: search result | query: {text}"
//
// If a different embedding model is ever configured these prefixes may not
// apply — they're kept here, separate from the transport Client, so swapping
// them out is a one-file change.

// DocumentText wraps a stored message in EmbeddingGemma's document prompt.
func DocumentText(text string) string {
	return "title: none | text: " + text
}

// QueryText wraps a search query in EmbeddingGemma's retrieval-query prompt.
func QueryText(text string) string {
	return "task: search result | query: " + text
}
