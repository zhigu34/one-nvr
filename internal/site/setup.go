package site

import (
	"context"
	"crypto/subtle"
	"fmt"
	"github.com/jackc/pgx/v5"
	"github.com/zhigu34/one-nvr/internal/audit"
	"github.com/zhigu34/one-nvr/internal/auth"
	"github.com/zhigu34/one-nvr/internal/database"
	"github.com/zhigu34/one-nvr/internal/fault"
	"github.com/zhigu34/one-nvr/internal/id"
	"github.com/zhigu34/one-nvr/internal/secrets"
	"strings"
	"time"
	_ "time/tzdata"
)

var ErrInitialized = fault.New(409, "already_initialized", "站点已初始化")

type Site struct {
	ID           id.ID  `json:"id"`
	Name         string `json:"name"`
	Timezone     string `json:"timezone"`
	ChannelCount int    `json:"channel_count"`
	Version      int64  `json:"version"`
}
type SetupInput struct {
	Token         string `json:"token"`
	AdminName     string `json:"admin_name"`
	AdminPassword string `json:"admin_password"`
	Name          string `json:"name"`
	Timezone      string `json:"timezone"`
	ChannelCount  int    `json:"channel_count"`
}
type Service struct {
	DB        *database.DB
	Auth      *auth.Service
	Secrets   secrets.State
	Passwords *auth.PasswordHasher
}

func ValidateTimezone(name string) bool {
	if name == "" || name == "Local" || strings.Contains(name, "..") {
		return false
	}
	_, err := time.LoadLocation(name)
	return err == nil
}
func (s *Service) Initialized(ctx context.Context) (bool, error) {
	var exists bool
	err := s.DB.Pool.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM sites)").Scan(&exists)
	return exists, err
}
func (s *Service) Setup(ctx context.Context, in SetupInput) (Site, error) {
	var result Site
	if len(in.Token) != 64 || subtle.ConstantTimeCompare([]byte(in.Token), []byte(s.Secrets.SetupToken)) != 1 {
		return result, fault.New(403, "invalid_setup_token", "初始化令牌无效")
	}
	if !auth.ValidateUsername(in.AdminName) || auth.ValidatePassword(in.AdminPassword) != nil || !ValidateTimezone(in.Timezone) || (in.ChannelCount != 16 && in.ChannelCount != 32) || strings.TrimSpace(in.Name) == "" || len(in.Name) > 128 {
		return result, auth.ErrInvalid
	}
	if ready, err := s.Initialized(ctx); err != nil {
		return result, err
	} else if ready {
		return result, ErrInitialized
	}
	hash, err := s.Passwords.Hash(ctx, in.AdminPassword)
	if err != nil {
		return result, err
	}
	adminID, err := id.New()
	if err != nil {
		return result, err
	}
	err = s.DB.WithinTx(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(170019)"); err != nil {
			return err
		}
		var exists bool
		if err := tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM sites)").Scan(&exists); err != nil {
			return err
		}
		if exists {
			return ErrInitialized
		}
		result = Site{s.Secrets.SiteID, strings.TrimSpace(in.Name), in.Timezone, in.ChannelCount, 1}
		if _, err := tx.Exec(ctx, "INSERT INTO sites(id,name,timezone,channel_count) VALUES($1,$2,$3,$4)", result.ID, result.Name, result.Timezone, result.ChannelCount); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, "INSERT INTO users(id,site_id,username,password_hash,role) VALUES($1,$2,$3,$4,'admin')", adminID, result.ID, in.AdminName, hash); err != nil {
			return err
		}
		for n := 1; n <= in.ChannelCount; n++ {
			channelID, err := id.New()
			if err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, "INSERT INTO channels(id,site_id,channel_no,channel_name) VALUES($1,$2,$3,$4)", channelID, result.ID, n, fmt.Sprintf("通道 %02d", n)); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, "INSERT INTO channel_grants(user_id,channel_id,live,playback,export,configure) VALUES($1,$2,true,true,true,true)", adminID, channelID); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, "INSERT INTO recording_policies(channel_id) VALUES($1)", channelID); err != nil {
				return err
			}
		}
		return audit.Append(ctx, tx, audit.Entry{ActorID: adminID, Action: "site.initialized", ObjectID: result.ID})
	})
	return result, err
}
