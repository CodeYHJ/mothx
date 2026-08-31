package wechat

import (
	"context"
	"embed"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
)

// These fixtures are sanitized extracts of the locked Tencent 2.4.6 package.
// They contain no credentials, live URLs, media bytes, or platform references.
//
//go:embed testdata/tencent-2.4.6/*.json
var tencent246Fixtures embed.FS

func TestTencent246FixtureManifestAndWireContract(t *testing.T) {
	manifest := loadTencentFixture(t, "manifest.json")
	var metadata struct {
		Package        string   `json:"package"`
		Version        string   `json:"version"`
		Author         string   `json:"author"`
		NPMIntegrity   string   `json:"npmIntegrity"`
		TarballSHA512  string   `json:"tarballSHA512"`
		GitHead        string   `json:"gitHead"`
		EvidenceFiles  []string `json:"evidenceFiles"`
		SourceContract struct {
			GetUploadURLMethod string `json:"getUploadURLMethod"`
			CDNUploadMethod    string `json:"cdnUploadMethod"`
			NoNeedThumb        bool   `json:"noNeedThumb"`
			AESKeyEncoding     string `json:"aesKeyEncoding"`
			ImageMediaType     int    `json:"imageMediaType"`
			VideoMediaType     int    `json:"videoMediaType"`
			FileMediaType      int    `json:"fileMediaType"`
			AmbiguousClientID  string `json:"ambiguousClientID"`
		} `json:"sourceContract"`
		RealBotVerified bool `json:"realBotVerified"`
	}
	if err := json.Unmarshal(manifest, &metadata); err != nil {
		t.Fatal(err)
	}
	if metadata.Package != "@tencent-weixin/openclaw-weixin" || metadata.Version != "2.4.6" || metadata.Author != "Tencent" {
		t.Fatalf("Tencent fixture metadata = %#v", metadata)
	}
	if !strings.HasPrefix(metadata.NPMIntegrity, "sha512-") || len(metadata.TarballSHA512) != 128 || len(metadata.GitHead) != 40 {
		t.Fatalf("incomplete Tencent fixture provenance = %#v", metadata)
	}
	if metadata.RealBotVerified {
		t.Fatal("source fixture must not claim real Bot verification")
	}
	wantEvidenceFiles := []string{
		"src/api/api.ts",
		"src/api/types.ts",
		"src/cdn/upload.ts",
		"src/cdn/cdn-upload.ts",
		"src/messaging/send.ts",
	}
	if fmt.Sprint(metadata.EvidenceFiles) != fmt.Sprint(wantEvidenceFiles) {
		t.Fatalf("Tencent fixture evidence files = %v, want %v", metadata.EvidenceFiles, wantEvidenceFiles)
	}
	if metadata.SourceContract.GetUploadURLMethod != http.MethodPost ||
		metadata.SourceContract.CDNUploadMethod != http.MethodPost ||
		!metadata.SourceContract.NoNeedThumb ||
		metadata.SourceContract.AESKeyEncoding != "base64-ascii-hex" ||
		metadata.SourceContract.ImageMediaType != int(UploadMediaImage) ||
		metadata.SourceContract.VideoMediaType != int(UploadMediaVideo) ||
		metadata.SourceContract.FileMediaType != int(UploadMediaFile) ||
		metadata.SourceContract.AmbiguousClientID != "uncertain" {
		t.Fatalf("Tencent source contract = %#v", metadata.SourceContract)
	}

	var wire WireMessage
	if err := json.Unmarshal(loadTencentFixture(t, "inbound-media.json"), &wire); err != nil {
		t.Fatal(err)
	}
	if wire.MessageID != 42 || wire.MessageType != MessageTypeUser || wire.ContextToken == "" || len(wire.ItemList) != 5 {
		t.Fatalf("Tencent inbound fixture = %#v", wire)
	}
	attachments := NewBot(BotOptions{}).inboundAttachments(&wire)
	if len(attachments) != 5 {
		t.Fatalf("fixture attachment count = %d, want image/voice/file/video/quoted file", len(attachments))
	}
	wantKinds := []string{"image", "audio", "file", "video", "file"}
	for index, attachment := range attachments {
		if string(attachment.Kind) != wantKinds[index] {
			t.Fatalf("fixture attachment %d kind = %q, want %q", index, attachment.Kind, wantKinds[index])
		}
		if strings.Contains(attachment.Reference, "fixture-") {
			t.Fatalf("opaque fixture reference leaked into item key: %q", attachment.Reference)
		}
	}
}

func TestTencent246FixtureAESKeyEncodingMatchesPackageContract(t *testing.T) {
	var payload struct {
		Msg struct {
			ItemList []struct {
				ImageItem *struct {
					Media *struct {
						AESKey string `json:"aes_key"`
					} `json:"media"`
				} `json:"image_item"`
			} `json:"item_list"`
		} `json:"msg"`
	}
	if err := json.Unmarshal(loadTencentFixture(t, "sendmessage-image.json"), &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Msg.ItemList) != 1 || payload.Msg.ItemList[0].ImageItem == nil || payload.Msg.ItemList[0].ImageItem.Media == nil {
		t.Fatalf("image fixture payload = %#v", payload)
	}
	encoded := payload.Msg.ItemList[0].ImageItem.Media.AESKey
	decoded, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil || string(decoded) != "30313233343536373839616263646566" {
		t.Fatalf("fixture aes_key decodes to %q, err=%v; want ASCII hex", decoded, err)
	}
	key, err := DecodeAESKey(encoded)
	if err != nil || string(key) != "0123456789abcdef" {
		t.Fatalf("DecodeAESKey fixture = %x, err=%v", key, err)
	}
}

func TestTencent246FixtureUploadHTTPContract(t *testing.T) {
	var fixtureRequest map[string]any
	if err := json.Unmarshal(loadTencentFixture(t, "getuploadurl-request.json"), &fixtureRequest); err != nil {
		t.Fatal(err)
	}
	fixtureResponse := loadTencentFixture(t, "getuploadurl-response.json")
	var gotRequest map[string]any
	client := NewClient()
	client.HTTP = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.Method != http.MethodPost || request.URL.Path != "/ilink/bot/getuploadurl" {
			return nil, fmt.Errorf("fixture request = %s %s", request.Method, request.URL.Path)
		}
		if request.Header.Get("Authorization") != "Bearer fixture-token" || request.Header.Get("AuthorizationType") != "ilink_bot_token" {
			return nil, fmt.Errorf("fixture auth headers are incomplete")
		}
		body, err := io.ReadAll(request.Body)
		if err != nil {
			return nil, err
		}
		if err := json.Unmarshal(body, &gotRequest); err != nil {
			return nil, err
		}
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(string(fixtureResponse))), Request: request}, nil
	})}
	response, err := client.GetUploadURL(context.Background(), "https://ilink.example.invalid", "fixture-token", GetUploadURLRequest{
		FileKey: fixtureRequest["filekey"].(string), MediaType: UploadMediaType(fixtureRequest["media_type"].(float64)),
		ToUserID: fixtureRequest["to_user_id"].(string), RawSize: int64(fixtureRequest["rawsize"].(float64)),
		RawFileMD5: fixtureRequest["rawfilemd5"].(string), FileSize: int64(fixtureRequest["filesize"].(float64)),
		NoNeedThumb: fixtureRequest["no_need_thumb"].(bool), AESKey: fixtureRequest["aeskey"].(string),
	})
	if err != nil {
		t.Fatal(err)
	}
	if response.UploadParam != "fixture-upload-param" || response.UploadFullURL == "" {
		t.Fatalf("fixture response = %#v", response)
	}
	for _, key := range []string{"filekey", "media_type", "to_user_id", "rawsize", "rawfilemd5", "filesize", "no_need_thumb", "aeskey"} {
		if fmt.Sprint(gotRequest[key]) != fmt.Sprint(fixtureRequest[key]) {
			t.Fatalf("request field %s = %#v, want %#v", key, gotRequest[key], fixtureRequest[key])
		}
	}
}

func loadTencentFixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := tencent246Fixtures.ReadFile("testdata/tencent-2.4.6/" + name)
	if err != nil {
		t.Fatalf("read Tencent fixture %s: %v", name, err)
	}
	return data
}

func TestTencent246FixtureIntegrityValueIsSelfConsistent(t *testing.T) {
	var metadata struct {
		NPMIntegrity  string `json:"npmIntegrity"`
		TarballSHA512 string `json:"tarballSHA512"`
	}
	if err := json.Unmarshal(loadTencentFixture(t, "manifest.json"), &metadata); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(metadata.NPMIntegrity, "sha512-") {
		t.Fatal("fixture integrity does not use npm sha512 format")
	}
	// Keep the expected registry values explicit so accidental edits to the
	// provenance manifest fail locally instead of silently changing the source.
	const wantNPMIntegrity = "sha512-qw9k3PLTiMWGNjjsknHgcTManH1w4j+Ji1ArWIaYLKCq3aFRsVwcqnPi127bvOoVMJGW4dbyJ8NECEMgoO+iRw=="
	const wantTarballSHA512 = "ab0f64dcf2d388c5863638ec9271e071331a9c7d70e23f898b502b5886982ca0aadda151b15c1caa73e2d76edbbcea15309196e1d6f227c344084320a0efa247"
	if metadata.NPMIntegrity != wantNPMIntegrity {
		t.Fatalf("npm integrity = %q, want %q", metadata.NPMIntegrity, wantNPMIntegrity)
	}
	if metadata.TarballSHA512 != wantTarballSHA512 {
		t.Fatalf("tarball sha512 = %q, want %q", metadata.TarballSHA512, wantTarballSHA512)
	}
	if strings.TrimSpace(fmt.Sprint(metadata.NPMIntegrity)) == "" {
		t.Fatal("empty npm integrity")
	}
}
