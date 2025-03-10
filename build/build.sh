#! /bin/bash
set -e

HERE=$(cd ${0%/*}; pwd)
ROOT=$(cd ${HERE}/../; pwd)
BUILD_MODE=local

EXPLICIT_BUILD_TARGET=
BUILD_IMAGE=
BUILD_CLI=
BUILD_CHART=
while [ -n "${1}" ] ; do
	ARG=${1}; shift
	case ${ARG} in
		--test)
			BUILD_MODE=test
			;;
		--release)
			BUILD_MODE=release
			;;
		--debug)
			export DEBUG=debug
			;;
		--ide)
			IDE_TYPE=${1}; shift
			;;
		image)
			BUILD_IMAGE=true
			EXPLICIT_BUILD_TARGET=true
			;;
		cli)
			BUILD_CLI=true
			EXPLICIT_BUILD_TARGET=true
			;;
		chart)
			BUILD_CHART=true
			EXPLICIT_BUILD_TARGET=true
			;;
		admin-tools)
			BUILD_ADMIN_TOOLS=true
			EXPLICIT_BUILD_TARGET=true
			;;
	esac
done

case "${IDE_TYPE}" in
	vscode)
		echo "generating configuration for IDE ${IDE_TYPE} ..."
		ROOT=${ROOT} ${HERE}/lib/ideconfig-${IDE_TYPE}.sh
		exit
	;;
	"") ;;
	*)
		echo "unknown IDE: ${IDE_TYPE}" >&2
		exit 1
	;;
esac

case ${BUILD_MODE} in
	release)
		if [ -n "${DEBUG}" ] ; then
			echo "ERROR: --debug and --release are exclusive." >&2
			exit 1
		fi

		export IMAGE_REGISTRY=${IMAGE_REGISTRY:-ghcr.io}
		REPOSITORY=${REPOSITORY:-opst/knitfab}
		export ARCH=${ARCH:-"arm64 amd64"}
		export OS=${OS:-"linux windows darwin"}
	;;
	local|test)
		export IMAGE_REGISTRY=${IMAGE_REGISTRY:-}
		REPOSITORY=${REPOSITORY:-local}
		export ARCH=${ARCH:-local}
		export OS=${OS:-}
	;;
	*)
		echo "unknown BUILD_MODE: ${BUILD_MODE}" >&2
		exit 1
	;;
esac
export BUILD_MODE


case ${REPOSITORY} in
	git://*)
		# git repository
		REMOTE_URL=$(git remote get-url ${REPOSITORY:6})
		case ${REMOTE_URL} in
			https://*/*.git)
				REPOSITORY=${REMOTE_URL#https://*/}
				REPOSITORY=${REPOSITORY%.git}
				;;
			*@*:*.git)
				REPOSITORY=${REMOTE_URL#*@*:}
				REPOSITORY=${REPOSITORY%.git}
				;;
			*)
				echo "unknown repository: ${REMOTE_URL}" >&2
				exit 1
				;;
		esac
		echo "detected REPOSITORY=${REPOSITORY}"
		;;
esac
export REPOSITORY

if [ -z "${EXPLICIT_BUILD_TARGET}" ] ; then
	BUILD_IMAGE=true
	BUILD_CLI=true
	BUILD_CHART=true
	BUILD_ADMIN_TOOLS=true
fi

function detect_diff() {
	cd ${ROOT}
	(
		cat .dockerignore
		echo "!build"
	) \
	| grep -Ee '^!' \
	| while read LINE; do
		if ! git diff --quiet -- ${LINE/!/} ; then
			echo "diff-"$(date +%Y%m%d%H%M%S)
			break
		fi
	done
}

if [ -z "${CHART_VERSION}" ] ; then
	CHART_VERSION=$(head -n 1 ${ROOT}/VERSION)
fi
export CHART_VERSION

echo "prepareing build environment..."
mkdir -p ${ROOT}/bin
for CMD in ${ROOT} ${ROOT}/cmd/* ; do
	(
		echo "generating code for ${CMD}..."
		cd ${CMD}
		go mod tidy
		go generate ./...
	)
done

export HASH=$(git rev-parse --short HEAD)
export LONG_HASH=$(git rev-parse HEAD)
if [ -z "${APP_VERSION}" ] ; then
	APP_VERSION=${CHART_VERSION}

	APP_VERSION=${APP_VERSION}-${HASH}
	LOCALDIFF=$(detect_diff)
	if [ -n "${LOCALDIFF}" ] ; then
		if [ "release" = "${BUILD_MODE}" ] ; then
			echo "ERROR: local diff is detected in release mode." >&2
			exit 1
		fi

		APP_VERSION=${APP_VERSION}-${LOCALDIFF}
	fi
fi

if [ "release" != "${BUILD_MODE}" ] ; then
	APP_VERSION=${APP_VERSION}-${BUILD_MODE}
fi
if [ -n "${DEBUG}" ] ; then
	APP_VERSION=${APP_VERSION}-debug
fi

export APP_VERSION

mkdir -p ${ROOT}/bin
mkdir -p ${ROOT}/charts/${BUILD_MODE}

if [ "local" = ${BUILD_MODE} ] ; then
	echo ${CHART_VERSION} > ${ROOT}/charts/${BUILD_MODE}/CHART_VERSION
fi

if [ -n "${BUILD_IMAGE}" ] ; then
	echo "*** building images ***" >&2
	if [ "local" = ${BUILD_MODE} ] ; then
		mkdir -p ${ROOT}/charts/${BUILD_MODE}/images
		IMAGE_INDEX=${ROOT}/charts/${BUILD_MODE}/images/index
		: > ${IMAGE_INDEX}
	else
		IMAGE_INDEX=/dev/null
	fi
	${ROOT}/build/lib/images.sh | tee ${IMAGE_INDEX}
fi

if [ -n "${BUILD_CLI}" ] ; then
	echo "*** building CLI ***" >&2
	${ROOT}/build/lib/cli.sh
fi

if [ -n "${BUILD_CHART}" ] ; then
	echo "*** building charts ***" >&2
	${ROOT}/build/lib/chart.sh
fi

if [ -n "${BUILD_ADMIN_TOOLS}" ] ; then
	echo "*** packaging admin tools ***" >&2

	tar -cz -f ${ROOT}/bin/admin-tools.tar.gz -C ${ROOT} admin-tools
fi

if [ "release" = "${BUILD_MODE}" ] ; then
	cat <<EOF >&2
*** THIS IS RELEASE BUILD ***

You have more tasks to release!

To publish images, run the following command:

    bash ./bin/images/publish.sh

To publish charts, and finish release.

    1. Commit and push the changes to develop branch, create Pull Request.
    2. Create Pull Request from develop to main branch.
    3. Set a tag with the version number.
    4. Make release in GitHub.
        - Write release note and put CLIs with it (they are in ./build/bin/clis).

EOF
fi
