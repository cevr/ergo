package lower

// Generated from the laws of internal/sketch/std/hmac by the TM lane's
// lawgen script; the probe restates each law as an Ergo test, and the
// statements are the ledger's (fu-ledger-runner).

// stdHmacLawsProbe restates the hmac laws whose runner is
// TestStdKeyedLawsRun as Ergo tests over std hmac.
const stdHmacLawsProbe = `import hex
import hmac
import rand
import strings
import time
import uuid

// vector and unhex restate hmac's own (the probe stands outside it).
type vector { Key string, Data string, Tag256 string, Tag512 string }

func unhex(s string) string { hex.Decode(s) catch { case hex.Malformed: "" } }

// failsAt restates uuid's: where Parse refuses s, or -1 when it reads it.
func failsAt(s string) int {
	let _ = uuid.Parse(s) catch {
	case uuid.Malformed as e:
		return e.At
	}
	-1
}

test HmacEqualIsEquality(a string, b string) {
	assert hmac.Equal(a, b) == (a == b) && hmac.Equal(a, a)
}

test HmacSumHasSize(key string, message string) {
	assert len(hmac.Sum(hmac.SHA256, key, message)) == 32 && len(hmac.Sum(hmac.SHA512, key, message)) == 64 && len(hmac.Sum(hmac.SHA1, key, message)) == 20
}

test HmacTagIsSum(key string, message string) {
	let long = key + strings.Repeat("k", 129)
	assert hmac.Tag(hmac.Key(hmac.SHA1, key), message) == hmac.Sum(hmac.SHA1, key, message) && hmac.Tag(hmac.Key(hmac.SHA256, key), message) == hmac.Sum(hmac.SHA256, key, message) && hmac.Tag(hmac.Key(hmac.SHA512, key), message) == hmac.Sum(hmac.SHA512, key, message) && hmac.Tag(hmac.Key(hmac.SHA1, long), message) == hmac.Sum(hmac.SHA1, long, message) && hmac.Tag(hmac.Key(hmac.SHA512, long), message) == hmac.Sum(hmac.SHA512, long, message)
}

test HmacMatchesRFC4231() {
	let cases = []vector{
		vector{Key: "0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b", Data: "4869205468657265", Tag256: "b0344c61d8db38535ca8afceaf0bf12b881dc200c9833da726e9376c2e32cff7", Tag512: "87aa7cdea5ef619d4ff0b4241a1d6cb02379f4e2ce4ec2787ad0b30545e17cdedaa833b7d6b8a702038b274eaea3f4e4be9d914eeb61f1702e696c203a126854"},
		vector{Key: "4a656665", Data: "7768617420646f2079612077616e7420666f72206e6f7468696e673f", Tag256: "5bdcc146bf60754e6a042426089575c75a003f089d2739839dec58b964ec3843", Tag512: "164b7a7bfcf819e2e395fbe73b56e0a387bd64222e831fd610270cd7ea2505549758bf75c05a994a6d034f65f8f0e6fdcaeab1a34d4a6b4b636e070a38bce737"},
		vector{Key: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Data: "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd", Tag256: "773ea91e36800e46854db8ebd09181a72959098b3ef8c122d9635514ced565fe", Tag512: "fa73b0089d56a284efb0f0756c890be9b1b5dbdd8ee81a3655f83e33b2279d39bf3e848279a722c806b485a47e67c807b946a337bee8942674278859e13292fb"},
		vector{Key: "0102030405060708090a0b0c0d0e0f10111213141516171819", Data: "cdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcd", Tag256: "82558a389a443c0ea4cc819899f2083a85f0faa3e578f8077a2e3ff46729665b", Tag512: "b0ba465637458c6990e5a8c5f61d4af7e576d97ff94b872de76f8050361ee3dba91ca5c11aa25eb4d679275cc5788063a5f19741120c4f2de2adebeb10a298dd"},
		vector{Key: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Data: "54657374205573696e67204c6172676572205468616e20426c6f636b2d53697a65204b6579202d2048617368204b6579204669727374", Tag256: "60e431591ee0b67f0d8a26aacbf5b77f8e0bc6213728c5140546040f0ee37f54", Tag512: "80b24263c7c1a3ebb71493c1dd7be8b49b46d1f41b4aeec1121b013783f8f3526b56d037e05f2598bd0fd2215d6a1e5295e64f73f63f0aec8b915a985d786598"},
		vector{Key: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Data: "5468697320697320612074657374207573696e672061206c6172676572207468616e20626c6f636b2d73697a65206b657920616e642061206c6172676572207468616e20626c6f636b2d73697a6520646174612e20546865206b6579206e6565647320746f20626520686173686564206265666f7265206265696e6720757365642062792074686520484d414320616c676f726974686d2e", Tag256: "9b09ffa71b942fcb27635fbcd5b0e944bfdc63644f0713938a7f51535c3a35e2", Tag512: "e37b6a775dc87dbaa4dfa9f96e5e3ffddebd71f8867289865df5a32d20cdc944b6022cac3c4982b10d5eeb55c3e4de15134676fb6de0446065c97440fa8c6a58"},
	}
	for c in cases {
		if hex.Encode(hmac.Sum(hmac.SHA256, unhex(c.Key), unhex(c.Data))) != c.Tag256 || hex.Encode(hmac.Sum(hmac.SHA512, unhex(c.Key), unhex(c.Data))) != c.Tag512 {
			assert false
			return
		}
	}
	assert hex.Encode(hmac.Sum(hmac.SHA1, "Jefe", "what do ya want for nothing?")) == "effcdf6ae5eb2fa2d27416d5f184df9c259a7c79"
}
`

// stdHmacLawStatements is the statement of each law the probe
// restates, by ledger key; lawledger.Run holds it equal to the ledger.
var stdHmacLawStatements = map[string]string{
	"hmac TagIsSum":        `TagIsSum(key string, message string) { let long = key + strings.Repeat("k", 129) Tag(Key(SHA1, key), message) == Sum(SHA1, key, message) && Tag(Key(SHA256, key), message) == Sum(SHA256, key, message) && Tag(Key(SHA512, key), message) == Sum(SHA512, key, message) && Tag(Key(SHA1, long), message) == Sum(SHA1, long, message) && Tag(Key(SHA512, long), message) == Sum(SHA512, long, message) }`,
	"hmac EqualIsEquality": `EqualIsEquality(a string, b string) { Equal(a, b) == (a == b) && Equal(a, a) }`,
	"hmac SumHasSize":      `SumHasSize(key string, message string) { len(Sum(SHA256, key, message)) == 32 && len(Sum(SHA512, key, message)) == 64 && len(Sum(SHA1, key, message)) == 20 }`,
	"hmac MatchesRFC4231":  `MatchesRFC4231() { let cases = []vector{ vector{Key: "0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b", Data: "4869205468657265", Tag256: "b0344c61d8db38535ca8afceaf0bf12b881dc200c9833da726e9376c2e32cff7", Tag512: "87aa7cdea5ef619d4ff0b4241a1d6cb02379f4e2ce4ec2787ad0b30545e17cdedaa833b7d6b8a702038b274eaea3f4e4be9d914eeb61f1702e696c203a126854"}, vector{Key: "4a656665", Data: "7768617420646f2079612077616e7420666f72206e6f7468696e673f", Tag256: "5bdcc146bf60754e6a042426089575c75a003f089d2739839dec58b964ec3843", Tag512: "164b7a7bfcf819e2e395fbe73b56e0a387bd64222e831fd610270cd7ea2505549758bf75c05a994a6d034f65f8f0e6fdcaeab1a34d4a6b4b636e070a38bce737"}, vector{Key: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Data: "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd", Tag256: "773ea91e36800e46854db8ebd09181a72959098b3ef8c122d9635514ced565fe", Tag512: "fa73b0089d56a284efb0f0756c890be9b1b5dbdd8ee81a3655f83e33b2279d39bf3e848279a722c806b485a47e67c807b946a337bee8942674278859e13292fb"}, vector{Key: "0102030405060708090a0b0c0d0e0f10111213141516171819", Data: "cdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcd", Tag256: "82558a389a443c0ea4cc819899f2083a85f0faa3e578f8077a2e3ff46729665b", Tag512: "b0ba465637458c6990e5a8c5f61d4af7e576d97ff94b872de76f8050361ee3dba91ca5c11aa25eb4d679275cc5788063a5f19741120c4f2de2adebeb10a298dd"}, vector{Key: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Data: "54657374205573696e67204c6172676572205468616e20426c6f636b2d53697a65204b6579202d2048617368204b6579204669727374", Tag256: "60e431591ee0b67f0d8a26aacbf5b77f8e0bc6213728c5140546040f0ee37f54", Tag512: "80b24263c7c1a3ebb71493c1dd7be8b49b46d1f41b4aeec1121b013783f8f3526b56d037e05f2598bd0fd2215d6a1e5295e64f73f63f0aec8b915a985d786598"}, vector{Key: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Data: "5468697320697320612074657374207573696e672061206c6172676572207468616e20626c6f636b2d73697a65206b657920616e642061206c6172676572207468616e20626c6f636b2d73697a6520646174612e20546865206b6579206e6565647320746f20626520686173686564206265666f7265206265696e6720757365642062792074686520484d414320616c676f726974686d2e", Tag256: "9b09ffa71b942fcb27635fbcd5b0e944bfdc63644f0713938a7f51535c3a35e2", Tag512: "e37b6a775dc87dbaa4dfa9f96e5e3ffddebd71f8867289865df5a32d20cdc944b6022cac3c4982b10d5eeb55c3e4de15134676fb6de0446065c97440fa8c6a58"}, } for c in cases { if hex.Encode(Sum(SHA256, unhex(c.Key), unhex(c.Data))) != c.Tag256 || hex.Encode(Sum(SHA512, unhex(c.Key), unhex(c.Data))) != c.Tag512 { return false } } hex.Encode(Sum(SHA1, "Jefe", "what do ya want for nothing?")) == "effcdf6ae5eb2fa2d27416d5f184df9c259a7c79" }`,
}
