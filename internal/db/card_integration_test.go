// SPDX-License-Identifier: Apache-2.0
//go:build integration

package db

import (
	"context"
	"strings"
	"testing"

	"github.com/FreshLabDev/tg"

	"github.com/FreshLabDev/framy/internal/fx"
)

// The card holds the last sign-in's name and photo, keeps the person's other
// settings, and refuses a photo that is not Telegram's.
func TestCardIsTheLastSignIn(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	const id = 4201
	if err := store.Touch(ctx, tg.User{ID: id, FirstName: "Taras"}, nil); err != nil {
		t.Fatal(err)
	}
	if c, err := store.Card(ctx, id); err != nil || c != (Card{}) {
		t.Fatalf("before a sign-in: %+v, %v", c, err)
	}
	if err := store.SetCurrency(ctx, id, fx.UAH); err != nil {
		t.Fatal(err)
	}
	if err := store.SetCard(ctx, id, " Taras Shevchenko ", "https://t.me/i/userpic/320/taras.jpg"); err != nil {
		t.Fatal(err)
	}
	c, err := store.Card(ctx, id)
	if err != nil || c.Name != "Taras Shevchenko" || c.PhotoURL != "https://t.me/i/userpic/320/taras.jpg" || c.At.IsZero() {
		t.Fatalf("after a sign-in: %+v, %v", c, err)
	}
	if cur, _ := store.Currency(ctx, id); cur != fx.UAH {
		t.Errorf("the card took the currency with it: %q", cur)
	}
	if err := store.SetCard(ctx, id, "Taras", "https://cdn4.telesco.pe/file/taras.jpg"); err != nil {
		t.Fatal(err)
	}
	if c, _ := store.Card(ctx, id); c.PhotoURL != "https://cdn4.telesco.pe/file/taras.jpg" {
		t.Errorf("a photo on Telegram's CDN was kept as %q", c.PhotoURL)
	}
	for _, bad := range []string{
		"http://t.me/i/userpic/320/t.jpg", "https://evil.example/t.jpg", "https://t.me.evil.example/t.jpg",
		"https://telesco.pe.evil.example/t.jpg", "https://t.me:8443/t.jpg", "https://telegram.org/t.jpg", "javascript:alert(1)",
	} {
		if err := store.SetCard(ctx, id, "Taras", bad); err != nil {
			t.Fatal(err)
		}
		if c, _ := store.Card(ctx, id); c.PhotoURL != "" {
			t.Errorf("the photo %q was kept as %q", bad, c.PhotoURL)
		}
	}
	if err := store.SetCard(ctx, id, strings.Repeat("я", 300), ""); err != nil {
		t.Fatal(err)
	}
	if c, _ := store.Card(ctx, id); len([]rune(c.Name)) != 256 {
		t.Errorf("a long name is %d letters", len([]rune(c.Name)))
	}
	if err := store.SetCard(ctx, 4299, "Nobody", ""); err == nil {
		t.Fatal("a card was saved for a person core does not know")
	}
}
