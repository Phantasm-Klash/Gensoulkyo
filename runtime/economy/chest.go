package economy

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"math/rand"
)

var ErrEmptyChestPool = errors.New("chest pool has no rewards")

type RewardWeight struct {
	Code   string `json:"code"`
	Rarity string `json:"rarity"`
	Weight int    `json:"weight"`
}

type ChestResult struct {
	ServerSeed string `json:"server_seed"`
	RewardCode string `json:"reward_code"`
	Rarity     string `json:"rarity"`
}

func OpenChest(userID, poolID string, openingIndex int64, rewards []RewardWeight) (ChestResult, error) {
	total := 0
	for _, reward := range rewards {
		if reward.Weight > 0 {
			total += reward.Weight
		}
	}
	if total <= 0 {
		return ChestResult{}, ErrEmptyChestPool
	}

	seed := seedFor(userID, poolID, openingIndex)
	r := rand.New(rand.NewSource(int64(binary.BigEndian.Uint64(seed[:8]))))
	roll := r.Intn(total)
	running := 0
	for _, reward := range rewards {
		if reward.Weight <= 0 {
			continue
		}
		running += reward.Weight
		if roll < running {
			return ChestResult{
				ServerSeed: hex.EncodeToString(seed[:]),
				RewardCode: reward.Code,
				Rarity:     reward.Rarity,
			}, nil
		}
	}
	return ChestResult{}, ErrEmptyChestPool
}

func seedFor(userID, poolID string, openingIndex int64) [32]byte {
	payload := make([]byte, 0, len(userID)+len(poolID)+16)
	payload = append(payload, userID...)
	payload = append(payload, '|')
	payload = append(payload, poolID...)
	payload = append(payload, '|')
	var index [8]byte
	binary.BigEndian.PutUint64(index[:], uint64(openingIndex))
	payload = append(payload, index[:]...)
	return sha256.Sum256(payload)
}

