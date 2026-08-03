package context

import (
	"container/heap"
	"embed"
	"encoding/json"
	"regexp"
	"sort"
	"strings"
	"sync"
)

// DeepSeek V3's tokenizer is a byte-level BPE tokenizer. The vocabulary and
// merge ranks are embedded so token estimates are deterministic and do not
// require Python or a network connection at runtime.
//
//go:embed tokenizerdata/deepseek_v3_tokenizer.json
var deepSeekTokenizerFS embed.FS

// DeepSeek V3 applies regex splitting before byte-level BPE.
var deepSeekPreTokenPattern = regexp.MustCompile(`\p{N}{1,3}|[一-龥\x{3040}-\x{30ff}]+|[!"#$%&'()*+,\-./:;<=>?@\[\\\]^_` + "`" + `{|}~][A-Za-z]+|[^\r\n\p{L}\p{P}\p{S}]?[\p{L}\p{M}]+| ?[\p{P}\p{S}]+[\r\n]*|\s*[\r\n]+|\s+`)

func deepSeekPretokenize(text string) []string {
	matches := deepSeekPreTokenPattern.FindAllString(text, -1)
	if len(matches) == 0 && text != "" {
		return []string{text}
	}
	return matches
}

type deepSeekTokenizerFile struct {
	AddedTokens []struct {
		ID      int    `json:"id"`
		Content string `json:"content"`
	} `json:"added_tokens"`
	Model struct {
		Vocab  map[string]int `json:"vocab"`
		Merges []string       `json:"merges"`
	} `json:"model"`
}

type deepSeekTokenizer struct {
	vocab         map[string]int
	ranks         map[string]int
	addedByLength []string
}

var (
	deepSeekOnce sync.Once
	deepSeekTok  *deepSeekTokenizer
)

func loadDeepSeekTokenizer() *deepSeekTokenizer {
	deepSeekOnce.Do(func() {
		data, err := deepSeekTokenizerFS.ReadFile("tokenizerdata/deepseek_v3_tokenizer.json")
		if err != nil {
			return
		}
		var file deepSeekTokenizerFile
		if json.Unmarshal(data, &file) != nil || len(file.Model.Vocab) == 0 {
			return
		}
		ranks := make(map[string]int, len(file.Model.Merges))
		for i, merge := range file.Model.Merges {
			ranks[merge] = i
		}
		added := make(map[string]int, len(file.AddedTokens))
		for _, token := range file.AddedTokens {
			if token.Content != "" {
				added[token.Content] = token.ID
			}
		}
		addedByLength := make([]string, 0, len(added))
		for token := range added {
			addedByLength = append(addedByLength, token)
		}
		sort.Slice(addedByLength, func(i, j int) bool {
			return len(addedByLength[i]) > len(addedByLength[j])
		})
		deepSeekTok = &deepSeekTokenizer{vocab: file.Model.Vocab, ranks: ranks, addedByLength: addedByLength}
	})
	return deepSeekTok
}

// deepSeekTokenCount counts tokens using the distributed DeepSeek V3
// tokenizer. It intentionally counts plain text only; message framing is
// accounted for by the caller when constructing a request estimate.
func deepSeekTokenCount(text string) int {
	tok := loadDeepSeekTokenizer()
	if tok == nil || text == "" {
		return 0
	}
	total := 0
	for text != "" {
		if special, ok := deepSeekConsumeAddedToken(tok, text); ok {
			total++
			text = text[len(special):]
			continue
		}
		nextSpecial := len(text)
		for _, special := range tok.addedByLength {
			if pos := strings.Index(text, special); pos >= 0 && pos < nextSpecial {
				nextSpecial = pos
			}
		}
		chunk := text[:nextSpecial]
		for _, piece := range deepSeekPretokenize(chunk) {
			total += deepSeekBPETokenCount(tok, piece)
		}
		text = text[nextSpecial:]
	}
	return total
}

func deepSeekConsumeAddedToken(tok *deepSeekTokenizer, text string) (string, bool) {
	for _, special := range tok.addedByLength {
		if len(text) >= len(special) && text[:len(special)] == special {
			return special, true
		}
	}
	return "", false
}

func deepSeekBPETokenCount(tok *deepSeekTokenizer, piece string) int {
	// GPT-style byte-level encoding maps bytes to the printable Unicode range
	// used by tokenizer.json. This is the same reversible mapping used by the
	// Hugging Face ByteLevel pre-tokenizer.
	symbols := make([]string, 0, len(piece))
	for _, b := range []byte(piece) {
		symbols = append(symbols, string(deepSeekByteToRune(b)))
	}
	if len(symbols) == 0 {
		return 0
	}
	symbols = deepSeekBPEMerge(tok, symbols)
	count := 0
	for _, symbol := range symbols {
		if _, ok := tok.vocab[symbol]; ok {
			count++
		} else {
			// Keep the estimator conservative if a future tokenizer file has a
			// merge not represented in its vocabulary.
			count += len([]byte(symbol))
		}
	}
	return count
}

type deepSeekPair struct {
	left  int
	right int
	rank  int
}

type deepSeekPairHeap []deepSeekPair

func (h deepSeekPairHeap) Len() int { return len(h) }
func (h deepSeekPairHeap) Less(i, j int) bool {
	if h[i].rank != h[j].rank {
		return h[i].rank < h[j].rank
	}
	return h[i].left < h[j].left
}
func (h deepSeekPairHeap) Swap(i, j int) { h[i], h[j] = h[j], h[i] }

func (h *deepSeekPairHeap) Push(x any) {
	*h = append(*h, x.(deepSeekPair))
}

func (h *deepSeekPairHeap) Pop() any {
	old := *h
	n := len(old)
	x := old[n-1]
	*h = old[:n-1]
	return x
}

func deepSeekBPEMerge(tok *deepSeekTokenizer, symbols []string) []string {
	n := len(symbols)
	if n < 2 {
		return symbols
	}
	prev := make([]int, n)
	next := make([]int, n)
	active := make([]bool, n)
	for i := range symbols {
		prev[i] = i - 1
		next[i] = i + 1
		active[i] = true
	}
	next[n-1] = -1

	h := &deepSeekPairHeap{}
	heap.Init(h)
	pushPair := func(left, right int) {
		if left < 0 || right < 0 || !active[left] || !active[right] {
			return
		}
		if rank, ok := tok.ranks[symbols[left]+" "+symbols[right]]; ok {
			heap.Push(h, deepSeekPair{left: left, right: right, rank: rank})
		}
	}
	for i := 0; i+1 < n; i++ {
		pushPair(i, i+1)
	}

	activeCount := n
	for h.Len() > 0 && activeCount > 1 {
		pair := heap.Pop(h).(deepSeekPair)
		if !active[pair.left] || !active[pair.right] || next[pair.left] != pair.right {
			continue
		}
		rank, ok := tok.ranks[symbols[pair.left]+" "+symbols[pair.right]]
		if !ok || rank != pair.rank {
			continue
		}
		left, right := pair.left, pair.right
		symbols[left] += symbols[right]
		active[right] = false
		activeCount--
		after := next[right]
		next[left] = after
		if after >= 0 {
			prev[after] = left
		}
		pushPair(prev[left], left)
		pushPair(left, next[left])
	}

	merged := make([]string, 0, activeCount)
	for i := 0; i >= 0; i = next[i] {
		if active[i] {
			merged = append(merged, symbols[i])
		}
	}
	return merged
}

func deepSeekByteToRune(b byte) rune {
	// bytes that already have a printable representation
	if (b >= '!' && b <= '~') || (b >= 0xa1 && b <= 0xac) || (b >= 0xae && b <= 0xff) {
		return rune(b)
	}
	n := 0
	for i := byte(0); i < b; i++ {
		if !((i >= '!' && i <= '~') || (i >= 0xa1 && i <= 0xac) || (i >= 0xae && i <= 0xff)) {
			n++
		}
	}
	return rune(0x100 + n)
}
