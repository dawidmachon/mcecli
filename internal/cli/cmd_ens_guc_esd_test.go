// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package cli

import (
	"net/http"
	"strings"
	"testing"
)

func TestENSCallbacks(t *testing.T) {
	fakeSFMC(t, func(mux *http.ServeMux) {
		mux.HandleFunc("/platform/v1/ens-callbacks", func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(`[{"callbackId":"cb-1","callbackName":"Automation monitoring","status":"verified"}]`))
		})
	})
	code, out, _ := run(t, "ens", "callbacks")
	if code != exitOK {
		t.Fatalf("exit=%d out=%s", code, out)
	}
	e := envelope(t, out)
	if e["count"] != float64(1) {
		t.Fatalf("row expected: %v", e)
	}
}

func TestENSSubsRequiresCallbackID(t *testing.T) {
	fakeSFMC(t, func(mux *http.ServeMux) {})
	if code, _, _ := run(t, "ens", "subs"); code != exitUsage {
		t.Fatalf("subs without callbackId must refuse")
	}
}

func TestGUCList(t *testing.T) {
	fakeSFMC(t, func(mux *http.ServeMux) {
		mux.HandleFunc("/Service.asmx", func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(`<soap:Envelope xmlns:soap="http://www.w3.org/2003/05/soap-envelope">
<soap:Body><RetrieveResponseMsg xmlns="http://exacttarget.com/wsdl/partnerAPI">
<OverallStatus>OK</OverallStatus><RequestID>r1</RequestID>
<Results xsi:type="GlobalUnsubscribeCategory"><ID>1</ID><Name>Activist</Name><CategoryType>Global</CategoryType></Results>
</RetrieveResponseMsg></soap:Body></soap:Envelope>`))
		})
	})
	code, out, _ := run(t, "guc", "list")
	if code != exitOK {
		t.Fatalf("exit=%d out=%s", code, out)
	}
	d := envelope(t, out)["data"].([]any)[0].(map[string]any)
	if d["Name"] != "Activist" {
		t.Fatalf("row wrong: %v", d)
	}
}

func TestESDListSearchAndGet(t *testing.T) {
	fakeSFMC(t, func(mux *http.ServeMux) {
		mux.HandleFunc("/Service.asmx", func(w http.ResponseWriter, r *http.Request) {
			b := make([]byte, 8192)
			n, _ := r.Body.Read(b)
			if !strings.Contains(string(b[:n]), "EmailSendDefinition") {
				t.Errorf("wrong ObjectType: %s", string(b[:n]))
			}
			_, _ = w.Write([]byte(`<soap:Envelope xmlns:soap="http://www.w3.org/2003/05/soap-envelope">
<soap:Body><RetrieveResponseMsg xmlns="http://exacttarget.com/wsdl/partnerAPI">
<OverallStatus>OK</OverallStatus><RequestID>r1</RequestID>
<Results xsi:type="EmailSendDefinition"><CustomerKey>ESD_X</CustomerKey><Name>ESD X</Name></Results>
<Results xsi:type="EmailSendDefinition"><CustomerKey>OTHER</CustomerKey><Name>Other</Name></Results>
</RetrieveResponseMsg></soap:Body></soap:Envelope>`))
		})
	})
	code, out, _ := run(t, "esd", "list", "--search", "ESD_X")
	if code != exitOK {
		t.Fatalf("exit=%d out=%s", code, out)
	}
	if e := envelope(t, out); e["count"] != float64(1) {
		t.Fatalf("search filter failed: %v", e)
	}
	code, out, _ = run(t, "esd", "get", "ESD_X")
	if code != exitOK {
		t.Fatalf("exit=%d out=%s", code, out)
	}
	if d := dataMap(t, out); d["CustomerKey"] != "ESD_X" {
		t.Fatalf("client-side match failed: %v", d)
	}
}
