-- Four settings became fixed behaviour: the guide is always rebuilt on start, runs are kept
-- twenty deep, a channel gone from its playlist is forgotten after a fortnight, and channel
-- ids are always the "NFL 03" label form. Nothing reads these rows any more, so drop them
-- rather than leave values behind that look like they still decide something.
DELETE FROM settings WHERE key IN (
    'refresh_on_start',
    'keep_runs',
    'forget_channels_after_days',
    'channel_id_style'
);
