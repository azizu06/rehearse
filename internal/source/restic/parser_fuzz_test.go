package restic

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strconv"
	"strings"
	"testing"
)

const fuzzPrivateMarker = "private-fuzz-marker"

func FuzzDecodeVersionMessage(f *testing.F) {
	f.Add([]byte(`{"message_type":"version","version":"0.19.1","future":{"safe":true}}`))
	f.Add([]byte(`{"Message_Type":"version","version":"0.19.1"}`))
	f.Add([]byte(`{"message_type":"version","Version":"0.19.1"}`))
	f.Add([]byte(`{"message_type":"version","message_type":"future","version":"0.19.1"}`))
	f.Add([]byte(`{"message_type":"version","version":"0.17.0","version":"0.19.1"}`))
	f.Add([]byte(`{"message_type":"version","version":null}`))
	f.Add([]byte(`{"message_type":"version","Message_Type":"future","version":"0.19.1","Version":"ignored"}`))

	f.Fuzz(func(t *testing.T, data []byte) {
		version, err := decodeVersionMessage(data)
		if err != nil {
			if !errors.Is(err, errInvalidVersionMessage) {
				t.Fatalf("unexpected error: %v", err)
			}
			if strings.Contains(err.Error(), fuzzPrivateMarker) {
				t.Fatalf("private marker leaked through error: %v", err)
			}
			return
		}
		if !independentlyValidVersionMessage(data, version) {
			t.Fatalf("version parser accepted input rejected by independent JSON oracle: %q", data)
		}
	})
}

func independentlyValidVersionMessage(data []byte, wantVersion string) bool {
	decoder := json.NewDecoder(bytes.NewReader(data))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return false
	}
	messageTypeCount := 0
	versionCount := 0
	var messageType any
	var version any
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return false
		}
		name, ok := token.(string)
		if !ok {
			return false
		}
		var value any
		if err := decoder.Decode(&value); err != nil {
			return false
		}
		switch name {
		case "message_type":
			messageTypeCount++
			messageType = value
		case "version":
			versionCount++
			version = value
		}
	}
	if token, err = decoder.Token(); err != nil || token != json.Delim('}') {
		return false
	}
	if decoder.Decode(&struct{}{}) != io.EOF || messageTypeCount != 1 || versionCount != 1 {
		return false
	}
	messageTypeString, messageTypeOK := messageType.(string)
	versionString, versionOK := version.(string)
	return messageTypeOK && versionOK && messageTypeString == "version" && versionString != "" && versionString == wantVersion
}

func FuzzDecodeResticMessageType(f *testing.F) {
	f.Add([]byte(`{"message_type":"future_message","private":"` + fuzzPrivateMarker + `"}`))
	f.Add([]byte(`{"message_type":"summary"}`))
	f.Add([]byte(`{"private":"` + fuzzPrivateMarker + `"}`))
	f.Add([]byte(`{"message_type":null,"private":"` + fuzzPrivateMarker + `"}`))
	f.Add([]byte(`{"message_type":false,"private":"` + fuzzPrivateMarker + `"}`))
	f.Add([]byte(`{"messAge_tYpe":"future_message"}`))
	f.Add([]byte(`{"message_type":"future","message_type":"summary"}`))
	f.Add([]byte(`{"message_type":"summary","Message_Type":"future"}`))
	f.Add([]byte(`{"message_type":[]}`))
	f.Add([]byte(`{"message_type":"future_message","future":{"nested":[1,2]}}`))

	f.Fuzz(func(t *testing.T, data []byte) {
		messageType, err := decodeResticMessageType(data)
		if err != nil {
			if !errors.Is(err, errInvalidResticMessageType) {
				t.Fatalf("unexpected error: %v", err)
			}
			if strings.Contains(err.Error(), fuzzPrivateMarker) {
				t.Fatalf("private marker leaked through error: %v", err)
			}
			return
		}

		want, ok := independentlyValidMessageType(data)
		if !ok || messageType != want {
			t.Fatalf("message_type = %q, want valid string %q", messageType, want)
		}
	})
}

func independentlyValidMessageType(data []byte) (string, bool) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return "", false
	}
	messageTypeCount := 0
	var messageType any
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return "", false
		}
		name, ok := token.(string)
		if !ok {
			return "", false
		}
		var value any
		if err := decoder.Decode(&value); err != nil {
			return "", false
		}
		if name == "message_type" {
			messageTypeCount++
			messageType = value
		}
	}
	if token, err = decoder.Token(); err != nil || token != json.Delim('}') {
		return "", false
	}
	if decoder.Decode(&struct{}{}) != io.EOF || messageTypeCount != 1 {
		return "", false
	}
	value, ok := messageType.(string)
	return value, ok
}

func FuzzDecodeRestoreSummary(f *testing.F) {
	f.Add([]byte(`{"message_type":"summary"}`))
	f.Add([]byte(`{"message_type":"summary","files_restored":1,"total_files":1,"bytes_restored":2,"total_bytes":2}`))
	f.Add([]byte(`{"message_type":"summary","files_restored":1,"private":"` + fuzzPrivateMarker + `"}`))
	f.Add([]byte(`{"message_type":"summary","total_files":null,"private":"` + fuzzPrivateMarker + `"}`))
	f.Add([]byte(`{"message_type":"future_message"}`))
	f.Add([]byte(`{"message_type":"summary","Message_Type":"future","files_restored":1,"Files_Restored":999,"total_files":1}`))
	f.Add([]byte(`{"message_type":"summary","files_restored":2,"files_restored":0,"total_files":1}`))

	f.Fuzz(func(t *testing.T, data []byte) {
		summary, err := decodeRestoreSummary(data)
		want, valid := independentlyValidRestoreSummary(data)
		if err != nil {
			if !errors.Is(err, errInvalidRestoreSummary) {
				t.Fatalf("unexpected error: %v", err)
			}
			if strings.Contains(err.Error(), fuzzPrivateMarker) {
				t.Fatalf("private marker leaked through error: %v", err)
			}
			if valid {
				t.Fatalf("summary parser rejected input accepted by independent JSON oracle: %q", data)
			}
			return
		}
		if !valid || uint64(summary.FilesRestored) != want.filesRestored || uint64(summary.TotalFiles) != want.totalFiles || uint64(summary.BytesRestored) != want.bytesRestored || uint64(summary.TotalBytes) != want.totalBytes {
			t.Fatalf("summary parser accepted input rejected by independent JSON oracle: %q", data)
		}
	})
}

type oracleRestoreSummary struct {
	filesRestored uint64
	totalFiles    uint64
	bytesRestored uint64
	totalBytes    uint64
}

func independentlyValidRestoreSummary(data []byte) (oracleRestoreSummary, bool) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return oracleRestoreSummary{}, false
	}
	var summary oracleRestoreSummary
	messageTypeCount := 0
	seen := make(map[string]bool, 4)
	messageType := ""
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return oracleRestoreSummary{}, false
		}
		name, ok := token.(string)
		if !ok {
			return oracleRestoreSummary{}, false
		}
		var value any
		if err := decoder.Decode(&value); err != nil {
			return oracleRestoreSummary{}, false
		}
		if name == "message_type" {
			messageTypeCount++
			messageType, ok = value.(string)
			if !ok {
				return oracleRestoreSummary{}, false
			}
			continue
		}
		var target *uint64
		switch name {
		case "files_restored":
			target = &summary.filesRestored
		case "total_files":
			target = &summary.totalFiles
		case "bytes_restored":
			target = &summary.bytesRestored
		case "total_bytes":
			target = &summary.totalBytes
		default:
			continue
		}
		if seen[name] {
			return oracleRestoreSummary{}, false
		}
		seen[name] = true
		number, ok := value.(json.Number)
		if !ok {
			return oracleRestoreSummary{}, false
		}
		parsed, err := strconv.ParseUint(number.String(), 10, 64)
		if err != nil {
			return oracleRestoreSummary{}, false
		}
		*target = parsed
	}
	if token, err = decoder.Token(); err != nil || token != json.Delim('}') || decoder.Decode(&struct{}{}) != io.EOF {
		return oracleRestoreSummary{}, false
	}
	if messageTypeCount != 1 || messageType != "summary" || summary.filesRestored > summary.totalFiles || summary.bytesRestored > summary.totalBytes {
		return oracleRestoreSummary{}, false
	}
	return summary, true
}

func FuzzValidateStructuredExit(f *testing.F) {
	f.Add([]byte(`{"message_type":"exit_error","code":12}`))
	f.Add([]byte("{\"message_type\":\"future_error\",\"private\":\"" + fuzzPrivateMarker + "\"}\n{\"message_type\":\"exit_error\",\"code\":12}"))
	f.Add([]byte(`{"message_type":null,"private":"` + fuzzPrivateMarker + `"}`))
	f.Add([]byte(`{"message_type":"exit_error","code":10,"private":"` + fuzzPrivateMarker + `"}`))
	f.Add([]byte(`{"message_type":"exit_error","Code":12}`))
	f.Add([]byte(`{"message_type":"exit_error","code":null}`))
	f.Add([]byte(`{"message_type":"exit_error","code":"12"}`))
	f.Add([]byte(`{"message_type":"exit_error","code":12,"code":12}`))
	f.Add([]byte(`{"message_type":"future","message_type":"exit_error","code":12}`))
	f.Add([]byte(`{"code":12,"message_type":"exit_error","code":12}`))
	f.Add([]byte(`{"message_type":"exit_error","Message_Type":"future","code":12}`))
	f.Add([]byte(`{"message_type":"exit_error","code":12,"Code":12}`))
	f.Add([]byte(`{"message_type":"exit_error","code":10,"code":12}`))
	f.Add([]byte(`{"message_type":{"nested":"exit_error"},"code":12}`))
	f.Add([]byte(`{"message_type":"exit_error","code":[12]}`))
	f.Add([]byte(`{"message_type":"exit_error","code":12,"future":{"nested":[1,2]}}`))

	f.Fuzz(func(t *testing.T, data []byte) {
		err := validateStructuredExit(data, 12)
		if err == nil {
			if !independentlyValidStructuredExit(data, 12) {
				t.Fatalf("structured-exit parser accepted input rejected by independent JSON oracle: %q", data)
			}
			return
		}
		if !errors.Is(err, errInvalidStructuredExit) && !errors.Is(err, errInconsistentExitCode) && !errors.Is(err, errMissingStructuredExit) {
			t.Fatalf("unexpected error: %v", err)
		}
		if strings.Contains(err.Error(), fuzzPrivateMarker) {
			t.Fatalf("private marker leaked through error: %v", err)
		}
	})
}

// independentlyValidStructuredExit intentionally shares no production parser
// helpers or structs. It is the fuzz oracle for the accepted JSON-line shape.
func independentlyValidStructuredExit(data []byte, expectedCode int64) bool {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	exitErrorSeen := false
	for {
		token, err := decoder.Token()
		if errors.Is(err, io.EOF) {
			return exitErrorSeen
		}
		if err != nil || token != json.Delim('{') {
			return false
		}
		messageTypeCount := 0
		codeCount := 0
		var messageType any
		var code any
		for decoder.More() {
			token, err := decoder.Token()
			if err != nil {
				return false
			}
			name, ok := token.(string)
			if !ok {
				return false
			}
			var value any
			if err := decoder.Decode(&value); err != nil {
				return false
			}
			switch name {
			case "message_type":
				messageTypeCount++
				messageType = value
			case "code":
				codeCount++
				code = value
			}
		}
		if token, err = decoder.Token(); err != nil || token != json.Delim('}') {
			return false
		}
		messageTypeString, ok := messageType.(string)
		if messageTypeCount != 1 || !ok {
			return false
		}
		if messageTypeString != "exit_error" {
			continue
		}
		codeNumber, ok := code.(json.Number)
		if codeCount != 1 || !ok {
			return false
		}
		value, err := codeNumber.Int64()
		if err != nil || value != expectedCode {
			return false
		}
		exitErrorSeen = true
	}
}
