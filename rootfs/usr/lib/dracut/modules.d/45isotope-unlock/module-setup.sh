#!/bin/bash
# Isotope: ask for the disk passphrase in the initramfs when COS_PERSISTENT
# was encrypted with a passphrase (kcrypt method passphrase). See isotope-unlock.sh.

check() {
    require_binaries cryptsetup blkid systemd-ask-password || return 1
    return 0
}

depends() {
    # systemd-ask-password-console.path/.service come with dracut's systemd
    # module; immucore brings cryptsetup and blkid.
    echo systemd
    return 0
}

install() {
    declare systemdsystemunitdir=${systemdsystemunitdir}
    declare moddir=${moddir}
    declare initdir=${initdir}

    inst_multiple cryptsetup blkid systemd-ask-password systemd-tty-ask-password-agent sed
    inst_script "${moddir}/isotope-unlock.sh" /usr/bin/isotope-unlock
    inst_simple "${moddir}/isotope-unlock.service" "${systemdsystemunitdir}/isotope-unlock.service"
    mkdir -p "${initdir}/${systemdsystemunitdir}/initrd.target.wants"
    ln_r "${systemdsystemunitdir}/isotope-unlock.service" "${systemdsystemunitdir}/initrd.target.wants/isotope-unlock.service"

    # Who answers the question (the splash or the console agent) is
    # 51isotope-askpass's job.
    inst_multiple -o "${systemdsystemunitdir}/systemd-ask-password-console.path" \
        "${systemdsystemunitdir}/systemd-ask-password-console.service"
}
