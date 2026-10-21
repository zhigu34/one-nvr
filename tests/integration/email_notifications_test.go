package integration

// SMTP email as a notification channel kind. EVT-05.
//
// The adapter itself is unit-tested against a fake SMTP server (internal/notify).
// What is asserted here is the storage: the parameters an operator recognises
// from an NVR's Email tab round-trip, the password stays sealed, and a channel
// that could never deliver is refused at save time.

import (
	"context"
	"strings"
	"testing"

	"github.com/zhigu34/one-nvr/internal/auth"
	"github.com/zhigu34/one-nvr/internal/notify"
)

func emailChannelInput() notify.ChannelInput {
	recipients := []notify.RecipientInput{
		{Name: "值班", Address: "duty@example.com"},
		{Name: "", Address: "oncall@example.com"},
	}
	return notify.ChannelInput{
		Kind:    "email",
		Name:    "值班邮箱",
		Secret:  "smtp-password",
		Enabled: true,
		Email: &notify.SMTPConfig{
			SenderName:    "门岗值班",
			SenderAddress: "nvr@example.com",
			Host:          "smtp.example.com",
			Port:          587,
			TLS:           false,
			UseAuth:       true,
			Username:      "relay-user",
		},
		Recipients: &recipients,
	}
}

// The parameters an operator sets on an NVR's Email tab must come back unchanged.
func TestEmailChannelRoundTripsItsParameters(t *testing.T) {
	fixture := newNotifyFixture(t)
	ctx := context.Background()
	channel, err := fixture.notify.RegisterChannel(ctx, fixture.fixture.Admin, emailChannelInput())
	if err != nil {
		t.Fatal(err)
	}
	if channel.Email == nil {
		t.Fatal("an email channel was stored without its SMTP settings")
	}
	if channel.Email.SenderName != "门岗值班" ||
		channel.Email.SenderAddress != "nvr@example.com" ||
		channel.Email.Host != "smtp.example.com" ||
		channel.Email.Port != 587 ||
		!channel.Email.UseAuth ||
		channel.Email.Username != "relay-user" {
		t.Fatalf("SMTP settings did not round-trip: %+v", *channel.Email)
	}
	if len(channel.Recipients) != 2 {
		t.Fatalf("stored %d recipients, want 2", len(channel.Recipients))
	}
	// The password is a credential: the view reports that one exists, never what
	// it is.
	if !channel.HasSecret {
		t.Fatal("the SMTP password was not stored")
	}
	// Reading it back through the list path returns the same settings and the
	// same mailbox list.
	listed, err := fixture.notify.ListChannels(ctx, fixture.fixture.Admin)
	if err != nil {
		t.Fatal(err)
	}
	var found *notify.ChannelView
	for index := range listed {
		if listed[index].ID == channel.ID {
			found = &listed[index]
		}
	}
	if found == nil {
		t.Fatal("the email channel is missing from the list")
	}
	if found.Email == nil || found.Email.Port != 587 {
		t.Fatalf("the list lost the SMTP settings: %+v", found.Email)
	}
	if len(found.Recipients) != 2 || found.Recipients[0].Address != "duty@example.com" {
		t.Fatalf("the list lost the recipients: %+v", found.Recipients)
	}
}

// A channel that could never deliver is refused at save time, not at the first
// alarm: the operator is standing in front of the form.
func TestEmailChannelRefusesIncompleteSettings(t *testing.T) {
	fixture := newNotifyFixture(t)
	ctx := context.Background()
	cases := map[string]func(*notify.ChannelInput){
		"no SMTP block": func(in *notify.ChannelInput) { in.Email = nil },
		"no sender":     func(in *notify.ChannelInput) { in.Email.SenderAddress = "" },
		"no server":     func(in *notify.ChannelInput) { in.Email.Host = "" },
		"no port":       func(in *notify.ChannelInput) { in.Email.Port = 0 },
		"no username":   func(in *notify.ChannelInput) { in.Email.Username = "" },
		"no recipients": func(in *notify.ChannelInput) { empty := []notify.RecipientInput{}; in.Recipients = &empty },
		"blank recipient": func(in *notify.ChannelInput) {
			blank := []notify.RecipientInput{{Address: "  "}}
			in.Recipients = &blank
		},
		"recipient absent": func(in *notify.ChannelInput) { in.Recipients = nil },
	}
	for name, breakIt := range cases {
		in := emailChannelInput()
		copyEmail := *in.Email
		in.Email = &copyEmail
		breakIt(&in)
		if _, err := fixture.notify.RegisterChannel(ctx, fixture.fixture.Admin, in); err == nil {
			t.Fatalf("%s: an undeliverable email channel was accepted", name)
		}
	}
}

// Editing the mailbox list replaces it; leaving it out keeps it, so renaming a
// channel cannot silently unbind everyone.
func TestEmailRecipientsCanBeEditedWithoutTouchingTheRest(t *testing.T) {
	fixture := newNotifyFixture(t)
	ctx := context.Background()
	channel, err := fixture.notify.RegisterChannel(ctx, fixture.fixture.Admin, emailChannelInput())
	if err != nil {
		t.Fatal(err)
	}
	// Rename only: no recipients field at all.
	rename := emailChannelInput()
	rename.Recipients = nil
	rename.Name = "值班邮箱（新）"
	updated, err := fixture.notify.UpdateChannel(ctx, fixture.fixture.Admin, channel.ID, channel.Version, rename)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Name != "值班邮箱（新）" {
		t.Fatalf("the rename did not take: %q", updated.Name)
	}
	after, err := fixture.notify.ListChannels(ctx, fixture.fixture.Admin)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range after {
		if item.ID == channel.ID {
			if len(item.Recipients) != 2 {
				t.Fatalf("a rename dropped the recipients: %d left", len(item.Recipients))
			}
		}
	}
	// Now replace the list explicitly.
	replace := emailChannelInput()
	replace.Recipients = &[]notify.RecipientInput{{Name: "", Address: "new@example.com"}}
	if _, err := fixture.notify.UpdateChannel(ctx, fixture.fixture.Admin, channel.ID, updated.Version, replace); err != nil {
		t.Fatal(err)
	}
	after, err = fixture.notify.ListChannels(ctx, fixture.fixture.Admin)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range after {
		if item.ID == channel.ID {
			if len(item.Recipients) != 1 || item.Recipients[0].Address != "new@example.com" {
				t.Fatalf("the list was not replaced: %+v", item.Recipients)
			}
		}
	}
}

// A duplicate mailbox is stored once: the same person twice would receive two
// copies of every alarm.
func TestEmailRecipientsAreDeduplicated(t *testing.T) {
	fixture := newNotifyFixture(t)
	ctx := context.Background()
	in := emailChannelInput()
	in.Recipients = &[]notify.RecipientInput{
		{Address: "duty@example.com"},
		{Address: "DUTY@example.com"},
		{Address: "oncall@example.com"},
	}
	channel, err := fixture.notify.RegisterChannel(ctx, fixture.fixture.Admin, in)
	if err != nil {
		t.Fatal(err)
	}
	if len(channel.Recipients) != 2 {
		t.Fatalf("stored %d recipients for 2 distinct addresses: %+v", len(channel.Recipients), channel.Recipients)
	}
}

// The adapter registry reports what this build can deliver, and the UI offers
// exactly that. An email kind that is registered but missing here would be
// unreachable from the form.
func TestEmailKindIsRegistered(t *testing.T) {
	fixture := newNotifyFixture(t)
	kinds := fixture.notify.Registry().Kinds()
	found := false
	for _, kind := range kinds {
		if kind == "email" {
			found = true
		}
	}
	if !found {
		t.Fatalf("the email adapter is not registered; kinds are %v", kinds)
	}
}

// Deleting a channel removes its mailboxes with it: a recipient row pointing at a
// deleted channel would be unreachable data.
func TestDeletingAnEmailChannelRemovesItsRecipients(t *testing.T) {
	fixture := newNotifyFixture(t)
	ctx := context.Background()
	channel, err := fixture.notify.RegisterChannel(ctx, fixture.fixture.Admin, emailChannelInput())
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.notify.DeleteChannel(ctx, fixture.fixture.Admin, channel.ID, channel.Version); err != nil {
		t.Fatal(err)
	}
	var left int
	if err := fixture.fixture.DB.Pool.QueryRow(ctx, `SELECT count(*) FROM notification_recipients WHERE channel_id=$1`, channel.ID).Scan(&left); err != nil {
		t.Fatal(err)
	}
	if left != 0 {
		t.Fatalf("%d recipient rows survived their channel", left)
	}
}

// A viewer cannot create one: the channel holds a mail password.
func TestEmailChannelManagementRequiresAdmin(t *testing.T) {
	fixture := newNotifyFixture(t)
	ctx := context.Background()
	if _, err := fixture.notify.RegisterChannel(ctx, fixture.fixture.Admin, emailChannelInput()); err != nil {
		t.Fatal(err)
	}
	viewer, err := fixture.fixture.Auth.CreateUser(ctx, fixture.fixture.Admin, auth.CreateUserInput{
		Username: "email_viewer", Password: testPassword, Role: "viewer",
	})
	if err != nil {
		t.Fatal(err)
	}
	login, err := fixture.fixture.Auth.Login(ctx, "email_viewer", testPassword)
	if err != nil {
		t.Fatal(err)
	}
	principal := login.Principal
	_ = viewer
	if _, err := fixture.notify.RegisterChannel(ctx, principal, emailChannelInput()); err == nil {
		t.Fatal("a viewer created an email channel")
	}
	if _, err := fixture.notify.ListChannels(ctx, principal); err == nil {
		t.Fatal("a viewer listed the email channels")
	}
}

// A WeCom group robot stores its URL (which carries the key) and its layout.
func TestWeComChannelRoundTripsItsSettings(t *testing.T) {
	fixture := newNotifyFixture(t)
	ctx := context.Background()
	channel, err := fixture.notify.RegisterChannel(ctx, fixture.fixture.Admin, notify.ChannelInput{
		Kind:    "wecom",
		Name:    "值班群",
		URL:     "https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=robot-key",
		Enabled: true,
		WeCom:   &notify.WeComConfig{MsgType: "news", Mentions: "13800000000"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if channel.WeCom == nil {
		t.Fatal("a wecom channel was stored without its settings")
	}
	if channel.WeCom.MsgType != "news" || channel.WeCom.Mentions != "13800000000" {
		t.Fatalf("the robot settings did not round-trip: %+v", *channel.WeCom)
	}
	list, err := fixture.notify.ListChannels(ctx, fixture.fixture.Admin)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range list {
		if item.ID == channel.ID {
			if item.WeCom == nil || item.WeCom.MsgType != "news" {
				t.Fatalf("the list lost the robot settings: %+v", item.WeCom)
			}
			if item.URL == "" {
				t.Fatal("the list lost the robot URL, which is the only way to reach it")
			}
		}
	}
}

func TestWeComChannelRefusesIncompleteSettings(t *testing.T) {
	fixture := newNotifyFixture(t)
	ctx := context.Background()
	base := func() notify.ChannelInput {
		return notify.ChannelInput{
			Kind: "wecom", Name: "值班群",
			URL:     "https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=k",
			Enabled: true,
			WeCom:   &notify.WeComConfig{MsgType: "markdown"},
		}
	}
	cases := map[string]func(*notify.ChannelInput){
		"no URL":       func(in *notify.ChannelInput) { in.URL = "" },
		"no settings":  func(in *notify.ChannelInput) { in.WeCom = nil },
		"bad msg type": func(in *notify.ChannelInput) { in.WeCom.MsgType = "text" },
	}
	for name, breakIt := range cases {
		in := base()
		if in.WeCom != nil {
			copyWeCom := *in.WeCom
			in.WeCom = &copyWeCom
		}
		breakIt(&in)
		if _, err := fixture.notify.RegisterChannel(ctx, fixture.fixture.Admin, in); err == nil {
			t.Fatalf("%s: an undeliverable wecom channel was accepted", name)
		}
	}
}

// The URL carries the robot key: it must not appear in the audit trail.
func TestWeComRobotKeyNeverEntersTheAuditTrail(t *testing.T) {
	fixture := newNotifyFixture(t)
	ctx := context.Background()
	channel, err := fixture.notify.RegisterChannel(ctx, fixture.fixture.Admin, notify.ChannelInput{
		Kind: "wecom", Name: "值班群",
		URL:     "https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=robot-secret-key",
		Enabled: true,
		WeCom:   &notify.WeComConfig{MsgType: "markdown"},
	})
	if err != nil {
		t.Fatal(err)
	}
	rows, err := fixture.fixture.DB.Pool.Query(ctx, `SELECT details::text FROM audit_logs WHERE object_id=$1`, channel.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var details string
		if err := rows.Scan(&details); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(details, "robot-secret-key") {
			t.Fatalf("the robot key leaked into the audit trail: %s", details)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
}
