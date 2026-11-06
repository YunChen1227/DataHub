package parse

import (
	"strings"
	"testing"

	"github.com/datahub/relay/internal/domain/model"
)

const (
	nameMD5 = "ef8fd0d53cac67150e902266c0c2770f" // md5("陈韫")
	idMD5   = "12af2e69bc5b661677b7486d233215ee" // md5("440303200002163115")
)

func TestParseRiskPersonPlain(t *testing.T) {
	for _, enc := range []int{0, model.EncryptionPlain} {
		up, err := ParseRiskPerson(&model.QueryCommand{Name: " 张三 ", IDCard: "31000019900101001x", EncryptionType: enc})
		if err != nil {
			t.Fatalf("enc=%d: %v", enc, err)
		}
		if up.Name != "张三" || up.IDCard != "31000019900101001X" || up.PIIHashed {
			t.Fatalf("enc=%d 明文口径不对: %+v", enc, up)
		}
	}
	if _, err := ParseRiskPerson(&model.QueryCommand{Name: "张三", IDCard: idMD5, EncryptionType: model.EncryptionPlain}); err == nil {
		t.Fatal("明文模式下 32 位摘要应按身份证格式非法拦截")
	}
}

func TestParseRiskPersonMD5(t *testing.T) {
	up, err := ParseRiskPerson(&model.QueryCommand{
		Name: strings.ToUpper(nameMD5), IDCard: " " + idMD5 + " ", Mobile: "139xx", EncryptionType: model.EncryptionMD5,
	})
	if err != nil {
		t.Fatalf("MD5: %v", err)
	}
	if up.Name != nameMD5 || up.IDCard != idMD5 || !up.PIIHashed || up.Mobile != "" || up.Reqid == "" {
		t.Fatalf("MD5 应转小写透传且标 PIIHashed、不带 mobile: %+v", up)
	}
}

func TestParseRiskPersonRejects(t *testing.T) {
	cases := []struct {
		name string
		cmd  model.QueryCommand
	}{
		{"MD5 缺 name", model.QueryCommand{IDCard: idMD5, EncryptionType: model.EncryptionMD5}},
		{"MD5 name 是明文", model.QueryCommand{Name: "陈韫", IDCard: idMD5, EncryptionType: model.EncryptionMD5}},
		{"MD5 idCard 是明文", model.QueryCommand{Name: nameMD5, IDCard: "440303200002163115", EncryptionType: model.EncryptionMD5}},
		{"MD5 idCard 31 位", model.QueryCommand{Name: nameMD5, IDCard: idMD5[:31], EncryptionType: model.EncryptionMD5}},
		{"MD5 idCard 含非 hex", model.QueryCommand{Name: nameMD5, IDCard: "z" + idMD5[1:], EncryptionType: model.EncryptionMD5}},
		{"encryptionType=3", model.QueryCommand{Name: nameMD5, IDCard: idMD5, EncryptionType: 3}},
	}
	for _, c := range cases {
		if _, err := ParseRiskPerson(&c.cmd); err == nil {
			t.Fatalf("%s: 应拦截", c.name)
		}
	}
	if _, err := ParseRiskPerson(nil); err == nil {
		t.Fatal("nil 应拦截")
	}
}
