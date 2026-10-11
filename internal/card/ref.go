package card

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"log"
	"sync"

	"github.com/JonahR/simple-network/internal/demokeys"
)

var (
	refOnce sync.Once
	refKey  []byte
)

// Ref is a keyed one-way reference to a card number (HMAC-SHA256 under the
// key the network shares with its issuers). Unlike a plain hash it can't be
// reversed by trying every number in a BIN range, so it is safe to store
// and to send in place of the card number.
func Ref(pan string) string {
	refOnce.Do(func() {
		key, err := demokeys.Load("CARD_REF_KEY", demokeys.CardRef)
		if err != nil {
			log.Fatal(err)
		}
		refKey = key
	})
	m := hmac.New(sha256.New, refKey)
	m.Write([]byte(pan))
	return hex.EncodeToString(m.Sum(nil))
}
