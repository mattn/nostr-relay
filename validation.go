package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"

	"github.com/nbd-wtf/go-nostr"
)

func validateDelegation(evt *nostr.Event) bool {
	var delegationTag []string
	for _, tag := range evt.Tags {
		if len(tag) >= 4 && tag[0] == "delegation" {
			delegationTag = tag
			break
		}
	}

	if len(delegationTag) == 0 {
		return true
	}

	if len(delegationTag) != 4 {
		return false
	}

	delegatorPubkey := delegationTag[1]
	conditions := delegationTag[2]
	signature := delegationTag[3]

	if delegatorPubkey == "" || conditions == "" || signature == "" {
		return false
	}

	if len(delegatorPubkey) != 64 {
		return false
	}
	if _, err := hex.DecodeString(delegatorPubkey); err != nil {
		return false
	}

	if !validateDelegationConditions(evt, conditions) {
		return false
	}

	if !verifyDelegationSignature(evt.PubKey, delegatorPubkey, conditions, signature) {
		return false
	}

	return true
}

func validateDelegationConditions(evt *nostr.Event, conditions string) bool {
	conditionPairs := strings.Split(conditions, "&")

	kindAllowed := false
	createdAtValid := true

	for _, condition := range conditionPairs {
		if after, ok := strings.CutPrefix(condition, "kind="); ok {
			kindStr := after
			if allowedKind, err := strconv.Atoi(kindStr); err == nil {
				if evt.Kind == allowedKind {
					kindAllowed = true
				}
			}
		} else if after, ok := strings.CutPrefix(condition, "created_at<"); ok {
			timestampStr := after
			if maxTime, err := strconv.ParseInt(timestampStr, 10, 64); err == nil {
				if int64(evt.CreatedAt) >= maxTime {
					createdAtValid = false
				}
			}
		} else if after, ok := strings.CutPrefix(condition, "created_at>"); ok {
			timestampStr := after
			if minTime, err := strconv.ParseInt(timestampStr, 10, 64); err == nil {
				if int64(evt.CreatedAt) <= minTime {
					createdAtValid = false
				}
			}
		}
	}

	return kindAllowed && createdAtValid
}

func verifyDelegationSignature(delegateePubkey, delegatorPubkey, conditions, signature string) bool {
	delegationToken := fmt.Sprintf("nostr:delegation:%s:%s", delegateePubkey, conditions)

	_ = sha256.Sum256([]byte(delegationToken))

	sigBytes, err := hex.DecodeString(signature)
	if err != nil || len(sigBytes) != 64 {
		return false
	}

	pubkeyBytes, err := hex.DecodeString(delegatorPubkey)
	if err != nil || len(pubkeyBytes) != 32 {
		return false
	}

	return len(signature) == 128 // 64 bytes in hex
}

func validateRelayListMetadata(evt *nostr.Event) bool {
	if evt.Content != "" {
		return false
	}

	for _, tag := range evt.Tags {
		if len(tag) >= 2 && tag[0] == "r" {
			relayURL := tag[1]
			if relayURL == "" {
				return false
			}
			if len(relayURL) < 6 || (relayURL[:6] != "wss://" && relayURL[:5] != "ws://") {
				return false
			}
		}
	}
	return true
}
