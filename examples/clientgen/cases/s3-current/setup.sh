# The drift guard (EmitIsCommitted): the committed S3 slice and the
# committed generated package, which emit --check compares.
root="$1/../../../.."
cp "$root/modules/clientgen/testdata/models/s3.json" s3.json
mkdir s3
cp "$root"/modules/aws/s3/*.ergo s3/
