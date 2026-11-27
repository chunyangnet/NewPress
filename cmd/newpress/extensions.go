package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"newpress.local/newpress/internal/config"
	"newpress.local/newpress/internal/dirlock"
	"newpress.local/newpress/internal/extensionupdate"
	"newpress.local/newpress/internal/plugin"
	"newpress.local/newpress/internal/theme"
)

func isExtensionCommand(args []string) bool {
	return len(args) > 0 && (args[0] == "check-updates" || args[0] == "update" || args[0] == "rollback" || args[0] == "install" && len(args) > 2)
}

func extensionCommand(kind string, args []string) error {
	usage := "usage: newpress " + kind + " <check-updates [--preview]|update slug [--version x.y.z] [--preview]|rollback slug|install official-slug [--version x.y.z] [--preview]>"
	if len(args) == 0 {
		return errors.New(usage)
	}
	action, slug := args[0], ""
	if action != "check-updates" {
		if len(args) < 2 {
			return errors.New(usage)
		}
		slug, args = args[1], args[2:]
	} else {
		args = args[1:]
	}
	flags := flag.NewFlagSet(kind+" "+action, flag.ContinueOnError)
	preview := flags.Bool("preview", false, "include preview releases")
	targetVersion := flags.String("version", "", "select an exact release version")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || action == "check-updates" && *targetVersion != "" {
		return errors.New(usage)
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	locks, err := dirlock.Acquire(cfg.DataDir, cfg.ThemesDir, cfg.PluginsDir)
	if err != nil {
		return err
	}
	defer locks.Close()
	themes := theme.NewManager(cfg.ThemesDir, cfg.ActiveTheme, theme.WithSettingsDir(filepath.Join(cfg.DataDir, "theme-settings")))
	if err := themes.Recover(); err != nil {
		return err
	}
	if err := themes.Load(); err != nil && !(kind == "theme" && action == "rollback") {
		return err
	}
	plugins := plugin.NewManager(cfg.PluginsDir)
	if err := plugins.Recover(); err != nil {
		return err
	}
	market, err := plugin.NewMarket(plugins, plugin.EnvironmentSource())
	if err != nil {
		return err
	}
	var options []extensionupdate.Option
	if source, configured := plugin.EnvironmentThemeSource(); configured {
		themeMarket, err := plugin.NewThemeMarket(cfg.ThemesDir, source)
		if err != nil {
			return err
		}
		options = append(options, extensionupdate.WithThemeMarket(themeMarket))
	}
	service, err := extensionupdate.New(filepath.Join(cfg.DataDir, "extension-updates.json"), market, plugins, themes, options...)
	if err != nil {
		return err
	}
	defer service.Close()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	channel := "stable"
	if *preview {
		channel = "preview"
	}
	if action == "check-updates" {
		job, err := service.Start(extensionupdate.Request{Kind: kind, Action: "check", Channel: channel})
		if err != nil {
			return err
		}
		if _, err := waitExtensionJob(ctx, service, job); err != nil {
			return err
		}
		snapshot, err := service.Snapshot(kind, channel)
		if err != nil {
			return err
		}
		if kind == "plugin" {
			snapshot.Market.Themes = nil
		} else {
			snapshot.Market.Plugins = nil
		}
		return printJSON(snapshot)
	}
	if action != "rollback" {
		if err := market.Refresh(ctx); err != nil {
			return err
		}
	}
	snapshot, err := service.Snapshot(kind, channel)
	if err != nil {
		return err
	}
	for _, card := range snapshot.Cards {
		if card.Slug != slug {
			continue
		}
		request := extensionupdate.Request{Kind: kind, Action: action, Slug: slug, Channel: channel}
		if action != "install" {
			request.ExpectedVersion, request.Revision = card.Current, card.State.Revision
		}
		if action == "rollback" {
			request.Version = card.State.Previous
			if *targetVersion != "" {
				request.Version = *targetVersion
			}
		} else {
			for _, choice := range card.Releases {
				if choice.Allowed && choice.Action == action && (*targetVersion == "" || *targetVersion == choice.Version) {
					request.Version, request.Digest = choice.Version, choice.Digest
					break
				}
			}
			if request.Version == "" {
				return errors.New("没有符合通道、核心版本和数据兼容要求的发行版；请先运行 check-updates 查看详情")
			}
		}
		job, err := service.Start(request)
		if err != nil {
			return err
		}
		job, err = waitExtensionJob(ctx, service, job)
		if err != nil {
			return err
		}
		return printJSON(job)
	}
	return errors.New("扩展未安装或不在官方目录中")
}

func waitExtensionJob(ctx context.Context, service *extensionupdate.Service, initial extensionupdate.Job) (extensionupdate.Job, error) {
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	phase := ""
	for {
		job := service.Job(initial.Kind)
		if job.Phase != phase {
			fmt.Fprintln(os.Stderr, job.Message)
			phase = job.Phase
		}
		if !job.Busy() {
			if job.Phase != "succeeded" {
				return job, errors.New(job.Message)
			}
			return job, nil
		}
		select {
		case <-ctx.Done():
			_ = service.Cancel(initial.Kind, initial.ID)
			return job, ctx.Err()
		case <-ticker.C:
		}
	}
}
