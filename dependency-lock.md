# قفل وابستگی‌ها

> English: [dependency-lock.en.md](dependency-lock.en.md)

## Go

- نسخه هدف: `Go 1.27.1`
- archive رسمی Linux amd64: `go1.27.1.linux-amd64.tar.gz`
- SHA-256 ثبت‌شده: `63d339f0da5ab53635a56f2490a7984dfe12dfcff22ad749f63edaf590168445`
- CI با `actions/setup-go@v7` نسخه را از `go.mod` می‌خواند.

## YAML

- module: `go.yaml.in/yaml/v3`
- version: `v3.0.5`
- repository upstream: `yaml/go-yaml`
- license گزارش‌شده: Apache-2.0 و MIT
- module checksum:
  `h1:N6y/pJk8buWs9NY5ERU2HSMfm+IuD/OtfdAnq6kESPw=`
- go.mod checksum:
  `h1:HVTZu1O7/Vkt2N+BFy8Zza+lnLsABggaTM2ZpNIGuKg=`

checksumها توسط Go 1.27.1 در GitHub Actions تولید و سپس در `go.sum` commit شده‌اند.

## GitHub Actions

- `actions/checkout@v7`
- `actions/setup-go@v7`

## سیاست

هر dependency جدید باید version pin، license review، checksum/lock و تست روی toolchain هدف داشته باشد. dependency بدون دلیل روشن نباید به core اضافه شود.
