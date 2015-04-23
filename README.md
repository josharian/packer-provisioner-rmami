packer-provisioner-rmami is a [custom provisioner](https://www.packer.io/docs/extend/provisioner.html) for [packer](https://www.packer.io/).
It cleans up old AMIs. Packer says explicitly that Packer does not do this. Now you can ask it to. :)


### Use

rmami assumes that your AMIs are organized by Role tags. You can set this up by adding a section like this to your aws builder:

```json
"tags": {
  "Role": "some_role"
}
```

rmami currently only supports EBS-based AMIs, that is those created by "amazon-ebs" type builders).

To use rmami, add a provisioner like this:

```json
{
  "type": "rmami",
  "region": "us-east-1",
  "role": "some_role",
  "access_key": "your_aws_access_key",
  "secret_key": "your_aws_secret_key",
  "keep": "5",
  "dry_run": false
}
```


### Building and installation

This is vanilla Go. Build with `go build` or `go install`. Don't change the binary name. (It should be packer-provisioner-rmami.) Put the binary in your path. Packer will pick it up automatically.

