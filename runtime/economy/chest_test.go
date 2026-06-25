package economy

import "testing"

func TestOpenChestIsDeterministic(t *testing.T) {
	rewards := []RewardWeight{
		{Code: "card_a", Rarity: "common", Weight: 90},
		{Code: "card_b", Rarity: "rare", Weight: 10},
	}
	first, err := OpenChest("user", "pool", 1, rewards)
	if err != nil {
		t.Fatalf("OpenChest returned error: %v", err)
	}
	second, err := OpenChest("user", "pool", 1, rewards)
	if err != nil {
		t.Fatalf("OpenChest returned error: %v", err)
	}
	if first != second {
		t.Fatalf("expected deterministic result, got %#v and %#v", first, second)
	}
}

func TestOpenChestRejectsEmptyPool(t *testing.T) {
	if _, err := OpenChest("user", "pool", 1, nil); err != ErrEmptyChestPool {
		t.Fatalf("expected ErrEmptyChestPool, got %v", err)
	}
}

