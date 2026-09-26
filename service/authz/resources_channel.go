package authz

const (
	ResourceChannel = "channel"

	ActionRead           = "read"
	ActionOperate        = "operate"
	ActionWrite          = "write"
	ActionSensitiveWrite = "sensitive_write"
	ActionSecretView     = "secret_view"
	// tokeness-fitpolicy:begin （上游 merge 后请保留；见 docs/fitpolicy-tech-spec.md）
	// Capability marks are written by an automated suite through a controlled
	// applier, so they get their own actions instead of reusing ChannelWrite:
	// the generic write action also allows editing arbitrary non-sensitive
	// fields, which is far more than a suite applier should ever hold.
	ActionCapabilityWrite = "capability.write"
	ActionCapabilityForce = "capability.force"
	// tokeness-fitpolicy:end
)

var (
	ChannelRead           = Permission{Resource: ResourceChannel, Action: ActionRead}
	ChannelOperate        = Permission{Resource: ResourceChannel, Action: ActionOperate}
	ChannelWrite          = Permission{Resource: ResourceChannel, Action: ActionWrite}
	ChannelSensitiveWrite = Permission{Resource: ResourceChannel, Action: ActionSensitiveWrite}
	ChannelSecretView     = Permission{Resource: ResourceChannel, Action: ActionSecretView}
	// tokeness-fitpolicy:begin （上游 merge 后请保留；见 docs/fitpolicy-tech-spec.md）
	ChannelCapabilityWrite = Permission{Resource: ResourceChannel, Action: ActionCapabilityWrite}
	ChannelCapabilityForce = Permission{Resource: ResourceChannel, Action: ActionCapabilityForce}
	// tokeness-fitpolicy:end
)

func init() {
	RegisterResource(ResourceDefinition{
		Resource: ResourceChannel,
		LabelKey: "Channel Management",
		Actions: []ActionDefinition{
			{
				Action:         ActionRead,
				LabelKey:       "Read channels",
				DescriptionKey: "View channel lists and details without secrets.",
				DefaultRoles:   []string{BuiltInRoleAdmin},
			},
			{
				Action:         ActionOperate,
				LabelKey:       "Operate channels",
				DescriptionKey: "Test channels, refresh balances, and enable/disable individual, batch, or tagged channels.",
				DefaultRoles:   []string{BuiltInRoleAdmin},
			},
			{
				Action:         ActionWrite,
				LabelKey:       "Edit channel routing",
				DescriptionKey: "Edit non-sensitive settings such as models, groups, and routing rules.",
				DefaultRoles:   []string{BuiltInRoleAdmin},
			},
			{
				Action:         ActionSensitiveWrite,
				LabelKey:       "Edit sensitive channel settings",
				DescriptionKey: "Create channels or edit keys, base URLs, and overrides.",
			},
			{
				Action:         ActionSecretView,
				LabelKey:       "View channel secrets",
				DescriptionKey: "Reserved for viewing complete channel keys after secure verification.",
			},
			// tokeness-fitpolicy:begin （上游 merge 后请保留；见 docs/fitpolicy-tech-spec.md）
			// Deliberately has no default role: this is the action a controlled
			// suite applier holds, and it must be granted explicitly rather than
			// inherited by every administrator.
			{
				Action:         ActionCapabilityWrite,
				LabelKey:       "Write channel fit capabilities",
				DescriptionKey: "Record measured behaviour-level fit capabilities for a channel. Held by the controlled suite applier.",
			},
			// Granted to administrators by default because overriding a sticky
			// operator mark is an operational decision, not an automated one.
			{
				Action:         ActionCapabilityForce,
				LabelKey:       "Force channel fit capabilities",
				DescriptionKey: "Replace an unexpired manual fit-capability mark with a measured result.",
				DefaultRoles:   []string{BuiltInRoleAdmin},
			},
			// tokeness-fitpolicy:end
		},
	})
}
