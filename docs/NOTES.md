


This memo to explain how control is shared between Application and Release Object:

## Description

Hosted by both Release and Application. But independent.

## Usage

Application attribute, rendered in Release.Status 

## Roles/DependsOn

Effective values are the union of both Application and Release values (TODO : ensure they are in the model)

NB: module.dependsOn is different and handle intra application dependencies between modules

TODO: to implement

## Enabled

A module attribute. Default to 'true' Allow an application to disable a module at release time, using Parameters.
TODO: Implement

## Suspended: 

Release attribute. Suspend update from kubocd (!= HelmRelease.Spec.suspend, which may be set by SpecOverride)
TODO: Implement

## Protected: 
Release and Application attribute. The relevant value is the one from Release. Application provide a default/hint
TODO: Implements by setting status
TODO: Ensure some fallback if no webhook (Break ownership of helmReleases ?)

## Parameters
Provided by Release. Application provide default value computed from the schema

## Context
Provided by Release. Application provide default value computed from the schema

## TargetNamespace

The effective value is the one from the module. It is a template with {{.Release.targetNamespace | default .Release.metadata.namespace}} as default

## CreateNamespace 

A Release attribute

## SpecAddon

As a module attribute, a template to provide HelmRelease.spec base values. To be used to set other values than 
- chart
- values
- targetNamespace

## specAddonByModule

A release attribute

