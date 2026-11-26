package searchindex

import "encoding/json"

// fitReply retains the longest ranked prefix that fits the JSON-array budget.
func fitReply(hits []Hit, maxBytes int) ([]Hit, error) {
	if len(hits) == 0 {
		return hits, nil
	}

	var encoded replyByteCounter
	encoder := json.NewEncoder(&encoded)
	size, fitted := 2, 0 // Array brackets.
	for i := range hits {
		encoded = 0
		if err := encoder.Encode(&hits[i]); err != nil {
			return nil, err
		}
		size += int(encoded) - 1 // Encode adds a newline; Marshal does not.
		if i > 0 {
			size++ // Array comma.
		}
		if size <= maxBytes {
			fitted = i + 1
		}
		// Continue encoding even after overflow to preserve selected-set errors.
	}
	if fitted == 0 {
		// Even the best hit is larger than the reply budget: return it with
		// its text cut to fit instead of an error, which clients read as
		// "search broken" and retried (MCP-166).
		return truncatedFirst(hits[0], maxBytes)
	}
	return hits[:fitted], nil
}

const truncatedMark = "…[truncated]"

func truncatedFirst(h Hit, maxBytes int) ([]Hit, error) {
	text := h.Record.Text
	h.Record.EvidenceRefs = append([]string(nil), h.Record.EvidenceRefs...)
	h.Record.Text = ""
	base, err := json.Marshal([]Hit{h})
	if err != nil {
		return nil, err
	}
	room := maxBytes - len(base) - len(truncatedMark) - 8
	for room > 0 {
		cut := []rune(text)
		if len(cut) > room {
			cut = cut[:room]
		}
		h.Record.Text = string(cut) + truncatedMark
		out, err := json.Marshal([]Hit{h})
		if err != nil {
			return nil, err
		}
		if len(out) <= maxBytes {
			return []Hit{h}, nil
		}
		room -= len(out) - maxBytes
	}
	return nil, ErrBudgetExceeded
}

// Count encoded bytes without allocating a serialized copy of every hit.
type replyByteCounter int

func (n *replyByteCounter) Write(p []byte) (int, error) {
	*n += replyByteCounter(len(p))
	return len(p), nil
}
