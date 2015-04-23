// Packer-provisioner-rmami is a packer provisioner plugin.
// It deletes old AMIs, organized by tag.
package main

import (
	"errors"
	"fmt"
	"log"
	"os"
	"sort"
	"time"

	"github.com/awslabs/aws-sdk-go/aws"
	"github.com/awslabs/aws-sdk-go/service/ec2"
	"github.com/mitchellh/packer/common"
	"github.com/mitchellh/packer/packer"
	"github.com/mitchellh/packer/packer/plugin"
)

type plan struct {
	common.PackerConfig `mapstructure:",squash"`

	Region    string // the AWS region containing the old AMIs
	AccessKey string `mapstructure:"access_key"`
	SecretKey string `mapstructure:"secret_key"`
	Owner     string // owner of the AMIs to delete, if empty, uses the AccessKey's user
	Role      string // the tagged role to delete old AMIs for
	Keep      int    // the number of AMIs to keep, in addition to the newly created one
	DryRun    bool   `mapstructure:"dry_run"`

	tpl *packer.ConfigTemplate
}

func (p *plan) Prepare(raw ...interface{}) error {
	md, err := common.DecodeConfig(p, raw...)
	if err != nil {
		return err
	}
	errs := common.CheckUnusedConfig(md)

	p.tpl, err = packer.NewConfigTemplate()
	if err != nil {
		return err
	}
	p.tpl.UserVars = p.PackerUserVars

	// This is ugly and duplicative. Why?
	// I must be missing something, but
	// this is how all the standard provisioners do it. :/
	templates := map[string]*string{
		"region":     &p.Region,
		"access_key": &p.AccessKey,
		"secret_key": &p.SecretKey,
		"owner":      &p.Owner,
		"role":       &p.Role,
	}

	for n, ptr := range templates {
		var err error
		*ptr, err = p.tpl.Process(*ptr, nil)
		if err != nil {
			errs = packer.MultiErrorAppend(errs, fmt.Errorf("error processing %s: %s", n, err))
		}
	}
	if p.Role == "" {
		errs = packer.MultiErrorAppend(errs, fmt.Errorf("missing rmami provisioner parameter role"))
	}
	if p.Region == "" {
		errs = packer.MultiErrorAppend(errs, fmt.Errorf("missing rmami provisioner parameter region"))
	}
	if errs != nil && len(errs.Errors) > 0 {
		return errs
	}

	if p.Owner == "" {
		p.Owner = "self"
	}
	// There's no technical reason we can't delete all the old AMIs (keep==0 or keep==1),
	// but it's a bad idea, and it could happen by accident if
	// keep is left out of the packer config. Prevent that.
	if p.Keep < 2 {
		return errors.New("rmami provisioner parameter keep must be at least 2")
	}

	// TODO: template interpolation
	return nil
}

// ui.Say really should accept format strings to begin with.
func sayf(ui packer.Ui, msg string, v ...interface{}) {
	ui.Say(fmt.Sprintf(msg, v...))
}

func (p *plan) Provision(ui packer.Ui, comm packer.Communicator) error {
	sayf(ui, "Searching for AMIs in %q belonging to owner %q with tagged role %q", p.Region, p.Owner, p.Role)

	creds := aws.DetectCreds(p.AccessKey, p.SecretKey, "")
	cfg := aws.Config{
		Credentials: creds,
		Region:      p.Region,
	}
	svc := ec2.New(&cfg)

	in := ec2.DescribeImagesInput{
		Owners: []*string{aws.String(p.Owner)},
		Filters: []*ec2.Filter{
			{
				Name:   aws.String("tag:Role"),
				Values: []*string{aws.String(p.Role)},
			},
		},
	}

	resp, err := svc.DescribeImages(&in)
	if err != nil {
		return err
	}

	var imgs images
	for _, img := range resp.Images {
		i, err := newImage(img)
		if err != nil {
			return err
		}
		imgs = append(imgs, i)
	}

	sort.Sort(imgs)

	if len(imgs) <= p.Keep {
		sayf(ui, "Found %d AMIs. Keeping all of them.", len(imgs))
		return nil
	}

	sayf(ui, "Found %d AMIs. Keeping most recent %d.", len(imgs), p.Keep)
	for _, img := range imgs[:p.Keep] {
		sayf(ui, "Keeping %v, created at %v", img.id, img.created)
	}

	for _, img := range imgs[p.Keep:] {
		if p.DryRun {
			sayf(ui, "DRY RUN: Would delete %v, created at %v", img.id, img.created)
		} else {
			sayf(ui, "Deleting %v, created at %v", img.id, img.created)
			if err := img.delete(ui, svc); err != nil {
				// Don't bother trying to accumulate multiple errors.
				// If one fails, the others probably will too.
				return err
			}
		}
	}

	return nil
}

func (p *plan) Cancel() {
	log.Println("Cancelled")
	os.Exit(0)
}

func main() {
	server, err := plugin.Server()
	if err != nil {
		panic(err)
	}
	server.RegisterProvisioner(new(plan))
	server.Serve()
}

type image struct {
	id          string
	snapshotIds []string
	created     time.Time
}

type images []image

func (x images) Len() int           { return len(x) }
func (x images) Swap(i, j int)      { x[i], x[j] = x[j], x[i] }
func (x images) Less(i, j int) bool { return x[j].created.Before(x[i].created) }

func newImage(img *ec2.Image) (i image, err error) {
	if img.ImageID == nil {
		err = errors.New("no image id!")
		return
	}
	var t time.Time
	t, err = time.ParseInLocation(time.RFC3339, *img.CreationDate, time.UTC)
	if err != nil {
		return
	}
	i = image{id: *img.ImageID, created: t}
	for _, b := range img.BlockDeviceMappings {
		if b.EBS != nil && b.EBS.SnapshotID != nil {
			i.snapshotIds = append(i.snapshotIds, *b.EBS.SnapshotID)
		}
	}
	if len(i.snapshotIds) == 0 {
		err = fmt.Errorf("AMI %v does not have any associated snapshot IDs. rmami only supports EBS-based AMIs right now.", i.id)
		return
	}
	return
}

func (i image) delete(ui packer.Ui, svc *ec2.EC2) error {
	sayf(ui, "\t* deregistering image %v", i.id)
	_, err := svc.DeregisterImage(
		&ec2.DeregisterImageInput{ImageID: aws.String(i.id)},
	)
	if err != nil {
		return err
	}

	for _, sid := range i.snapshotIds {
		sayf(ui, "\t* deleting snapshot %v", sid)
		_, err := svc.DeleteSnapshot(
			&ec2.DeleteSnapshotInput{SnapshotID: aws.String(sid)},
		)
		if err != nil {
			return err
		}
	}

	return nil
}
