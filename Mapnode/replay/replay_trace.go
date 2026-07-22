package replay

import (
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"fmt"
	"io"
	"math"
	"math/big"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
)

type InvalidRowPolicy string

const (
	InvalidRowReject InvalidRowPolicy = "reject"
	InvalidRowDrop   InvalidRowPolicy = "drop"
)

type TraceOptions struct {
	InvalidRowPolicy InvalidRowPolicy
	ChainAllowlist   []string
	MaxEvents        uint64
}

type PreparedTrace struct {
	Digest      string
	SourceRows  uint64
	DroppedRows uint64
	Chains      []string
	InitHeights map[string]uint64
	Events      []ReplayEvent
}

func PrepareTrace(path string, options TraceOptions) (PreparedTrace, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return PreparedTrace{}, fmt.Errorf("read replay trace: %w", err)
	}
	digestBytes := sha256.Sum256(data)
	digest := hex.EncodeToString(digestBytes[:])

	reader := csv.NewReader(strings.NewReader(string(data)))
	reader.FieldsPerRecord = -1
	header, err := reader.Read()
	if err != nil {
		return PreparedTrace{}, fmt.Errorf("read replay trace header: %w", err)
	}
	columns := make(map[string]int, len(header))
	for index, name := range header {
		columns[strings.TrimSpace(name)] = index
	}
	required := []string{"src_chain", "dst_chain", "src_block_number", "dst_block_number", "src_block_time"}
	for _, name := range required {
		if _, ok := columns[name]; !ok {
			return PreparedTrace{}, fmt.Errorf("replay trace is missing required column %q", name)
		}
	}
	policy := options.InvalidRowPolicy
	if policy == "" {
		policy = InvalidRowReject
	}
	if policy != InvalidRowReject && policy != InvalidRowDrop {
		return PreparedTrace{}, fmt.Errorf("unknown invalid row policy %q", policy)
	}

	allowlist := make(map[string]struct{}, len(options.ChainAllowlist))
	for _, chain := range options.ChainAllowlist {
		name := canonicalChain(chain)
		if name != "" {
			allowlist[name] = struct{}{}
		}
	}

	trace := PreparedTrace{Digest: digest}
	for originalRow := uint64(1); ; originalRow++ {
		record, readErr := reader.Read()
		if readErr == io.EOF {
			break
		}
		trace.SourceRows++
		if readErr != nil {
			if policy == InvalidRowDrop {
				trace.DroppedRows++
				continue
			}
			return PreparedTrace{}, fmt.Errorf("read replay trace row %d: %w", originalRow, readErr)
		}
		event, parseErr := parseTraceRecord(record, columns, originalRow)
		if parseErr != nil {
			if policy == InvalidRowDrop {
				trace.DroppedRows++
				continue
			}
			return PreparedTrace{}, fmt.Errorf("invalid replay trace row %d: %w", originalRow, parseErr)
		}
		if len(allowlist) != 0 {
			_, sourceAllowed := allowlist[event.Source.Chain]
			_, destinationAllowed := allowlist[event.Destination.Chain]
			if !sourceAllowed || !destinationAllowed {
				continue
			}
		}
		trace.Events = append(trace.Events, event)
	}

	sort.SliceStable(trace.Events, func(left, right int) bool {
		a, b := trace.Events[left], trace.Events[right]
		if !a.SourceTime.Equal(b.SourceTime) {
			return a.SourceTime.Before(b.SourceTime)
		}
		if a.Destination.Chain != b.Destination.Chain {
			return a.Destination.Chain < b.Destination.Chain
		}
		if a.Destination.OriginalHeight != b.Destination.OriginalHeight {
			return a.Destination.OriginalHeight < b.Destination.OriginalHeight
		}
		if a.Source.Chain != b.Source.Chain {
			return a.Source.Chain < b.Source.Chain
		}
		if a.Source.OriginalHeight != b.Source.OriginalHeight {
			return a.Source.OriginalHeight < b.Source.OriginalHeight
		}
		return a.OriginalRow < b.OriginalRow
	})
	if options.MaxEvents > 0 && uint64(len(trace.Events)) > options.MaxEvents {
		trace.Events = trace.Events[:options.MaxEvents]
	}
	if len(trace.Events) == 0 {
		return PreparedTrace{}, fmt.Errorf("replay trace contains no prepared events")
	}

	trace.InitHeights = make(map[string]uint64)
	for _, event := range trace.Events {
		updateMinimum(trace.InitHeights, event.Source.Chain, event.Source.OriginalHeight)
		updateMinimum(trace.InitHeights, event.Destination.Chain, event.Destination.OriginalHeight)
	}
	trace.Chains = make([]string, 0, len(trace.InitHeights))
	for chain := range trace.InitHeights {
		trace.Chains = append(trace.Chains, chain)
	}
	sort.Strings(trace.Chains)
	for index := range trace.Events {
		event := &trace.Events[index]
		event.Sequence = uint64(index)
		event.Source.NormalizedHeight = event.Source.OriginalHeight - trace.InitHeights[event.Source.Chain]
		event.Destination.NormalizedHeight = event.Destination.OriginalHeight - trace.InitHeights[event.Destination.Chain]
		event.ID = ComputeEventID(trace.Digest, *event)
	}
	return trace, nil
}

func parseTraceRecord(record []string, columns map[string]int, originalRow uint64) (ReplayEvent, error) {
	value := func(column string) string {
		index, ok := columns[column]
		if !ok || index >= len(record) {
			return ""
		}
		return strings.TrimSpace(record[index])
	}
	sourceChain := canonicalChain(value("src_chain"))
	destinationChain := canonicalChain(value("dst_chain"))
	if sourceChain == "" || destinationChain == "" {
		return ReplayEvent{}, fmt.Errorf("source and destination chains are required")
	}
	sourceHeight, err := parseLegacyHeight(value("src_block_number"))
	if err != nil {
		return ReplayEvent{}, fmt.Errorf("source height: %w", err)
	}
	destinationHeight, err := parseLegacyHeight(value("dst_block_number"))
	if err != nil {
		return ReplayEvent{}, fmt.Errorf("destination height: %w", err)
	}
	sourceTime, err := parseLegacyTime(value("src_block_time"))
	if err != nil {
		return ReplayEvent{}, fmt.Errorf("source time: %w", err)
	}
	txCount := int64(1)
	if _, present := columns["tx_count"]; present {
		txCount = parseLegacyIntDefaultZero(value("tx_count"))
	}
	volumeUSD := 0.0
	if _, present := columns["volume_usd"]; present {
		volumeUSD = parseLegacyFloatDefaultZero(value("volume_usd"))
	}
	return ReplayEvent{
		OriginalRow: originalRow,
		Source:      ReplayBlock{Chain: sourceChain, OriginalHeight: sourceHeight},
		Destination: ReplayBlock{Chain: destinationChain, OriginalHeight: destinationHeight},
		SourceTime:  sourceTime,
		BridgeName:  strings.TrimSpace(value("bridge_name")),
		TxCount:     txCount,
		VolumeUSD:   volumeUSD,
	}, nil
}

func parseLegacyHeight(value string) (uint64, error) {
	text := strings.TrimSpace(value)
	if text == "" || len(text) > 1024 {
		return 0, fmt.Errorf("invalid non-negative numeric height %q", value)
	}
	if text[0] == '-' {
		return 0, fmt.Errorf("invalid non-negative numeric height %q", value)
	}
	if text[0] == '+' {
		text = text[1:]
	}
	exponent := int64(0)
	if exponentIndex := strings.IndexAny(text, "eE"); exponentIndex >= 0 {
		if strings.IndexAny(text[exponentIndex+1:], "eE") >= 0 {
			return 0, fmt.Errorf("invalid non-negative numeric height %q", value)
		}
		parsedExponent, err := strconv.ParseInt(text[exponentIndex+1:], 10, 32)
		if err != nil || parsedExponent < -1024 || parsedExponent > 1024 {
			return 0, fmt.Errorf("invalid non-negative numeric height %q", value)
		}
		exponent = parsedExponent
		text = text[:exponentIndex]
	}
	parts := strings.Split(text, ".")
	if len(parts) > 2 {
		return 0, fmt.Errorf("invalid non-negative numeric height %q", value)
	}
	whole := parts[0]
	fraction := ""
	if len(parts) == 2 {
		fraction = parts[1]
	}
	if whole == "" && fraction == "" {
		return 0, fmt.Errorf("invalid non-negative numeric height %q", value)
	}
	digits := whole + fraction
	for _, digit := range digits {
		if digit < '0' || digit > '9' {
			return 0, fmt.Errorf("invalid non-negative numeric height %q", value)
		}
	}
	coefficient := new(big.Int)
	if _, ok := coefficient.SetString(digits, 10); !ok {
		return 0, fmt.Errorf("invalid non-negative numeric height %q", value)
	}
	scale := int64(len(fraction)) - exponent
	result := new(big.Int).Set(coefficient)
	ten := big.NewInt(10)
	if scale <= 0 {
		factor := new(big.Int).Exp(ten, big.NewInt(-scale), nil)
		result.Mul(result, factor)
	} else {
		divisor := new(big.Int).Exp(ten, big.NewInt(scale), nil)
		quotient, remainder := new(big.Int), new(big.Int)
		quotient.QuoRem(result, divisor, remainder)
		twiceRemainder := new(big.Int).Lsh(remainder, 1)
		comparison := twiceRemainder.Cmp(divisor)
		if comparison > 0 || (comparison == 0 && quotient.Bit(0) == 1) {
			quotient.Add(quotient, big.NewInt(1))
		}
		result = quotient
	}
	maximum := new(big.Int).SetUint64(^uint64(0))
	if result.Sign() < 0 || result.Cmp(maximum) > 0 {
		return 0, fmt.Errorf("height %q exceeds uint64", value)
	}
	return result.Uint64(), nil
}

func parseLegacyTime(value string) (time.Time, error) {
	trimmed := strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(value), " UTC"))
	layouts := []string{
		time.RFC3339Nano,
		"2006-01-02 15:04:05.999999999Z07:00",
		"2006-01-02 15:04:05.999999999",
	}
	for _, layout := range layouts {
		parsed, err := time.Parse(layout, trimmed)
		if err == nil {
			return parsed.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("invalid timestamp %q", value)
}

func parseLegacyIntDefaultZero(value string) int64 {
	parsed, err := strconv.ParseFloat(value, 64)
	if err != nil || math.IsNaN(parsed) || math.IsInf(parsed, 0) {
		return 0
	}
	return int64(math.Round(parsed))
}

func parseLegacyFloatDefaultZero(value string) float64 {
	parsed, err := strconv.ParseFloat(value, 64)
	if err != nil || math.IsNaN(parsed) || math.IsInf(parsed, 0) {
		return 0
	}
	return parsed
}

func updateMinimum(values map[string]uint64, key string, candidate uint64) {
	current, ok := values[key]
	if !ok || candidate < current {
		values[key] = candidate
	}
}
