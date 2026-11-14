# CLAUDE.md

This file provides guidance to coding agents when working with code in this repository.

## Overview

Orchard is a small inventory service. It exposes an HTTP API backed by a relational store, and a nightly job that reconciles counts against the warehouse feed.

## Layout

- `cmd/orchard` is the server entry point.
- `internal/stock` holds the counting logic and its tests.
- `internal/feed` parses the warehouse feed.
- `migrations` holds the schema, one numbered file per change.

## Commands

    make build
    make test
    make lint

## Notes

The reconciliation job is idempotent. The feed parser tolerates trailing blank records. Schema changes are applied in order at start-up.
