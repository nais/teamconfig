# Teamconfig

Teamconfig is a command-line tool for managing team service accounts and
issuing Kubeconfig files.

Team service accounts enable *machine users* to access Kubernetes. These
service accounts live in the `default` namespace and have the name
`serviceuser-%s`, where `%s` is the team name. Access is granted by the RBAC
rolebinding `nais:developer` and further constrained by
[ToBAC](https://github.com/nais/tobac).

Service accounts are associated by a `Secret`, which contain a token that must
be placed in Kubeconfig files. These tokens can be rotated and revoked by
manipulating the service accounts. Teamconfig provides a simplified interface
to manage these service accounts and generate Kubeconfig files with the
neccessary tokens.

```
Usage of ./teamconfig:
      --clusters strings   Which clusters to operate on. (default [preprod-fss,preprod-sbs,prod-fss,prod-sbs])
      --create             Create teams that do not exist.
      --debug              Print debugging information.
      --revoke             Delete any tokens that belongs to this team.
      --rotate             Rotate secret tokens that are already present in cluster. This will invalidate old tokens.
      --team string        Team name that will own the configuration file.
```

## Retrieving a Kubeconfig file for a team

By default, running `teamconfig` will output the Kubeconfig to standard output.
Log messages will appear on stderr. All commands except `--revoke` will output
a Kubeconfig file.

```
./teamconfig --team XXX
```

## Creating a new team service user

Creating users is an idempotent action; nothing will happen if the service
account already exists.

```
./teamconfig --team XXX --create
```

## Rotating keys

If keys are lost, or simply too old, run the following command to generate new
keys. The old keys will be invalidated.

```
./teamconfig --team XXX --rotate
```

You may also combine this option with `--create`.

## Revoking keys

To remove a service user, run in revocation mode. No configuration will be generated.

```
./teamconfig --team XXX --revoke
```

## Output as JSON

Simply pipe the output to [yq](https://github.com/mikefarah/yq):

```
./teamconfig --team XXX | yq r - --tojson
```

## Developing

You need Golang >= 1.11 with `GO111MODULE=on`.

Run `make` in the repository root to build the binary.
