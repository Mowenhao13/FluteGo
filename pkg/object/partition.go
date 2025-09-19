package object

import (
	"Flute_go/pkg/tools"
	"log"
)

///
/// Block Partitioning Algorithm
/// See <https://www.rfc-editor.org/rfc/rfc5052#section-9.1>
///
///
/// This function implements the block partitioning algorithm as defined in RFC 5052.
/// The algorithm is used to partition a large amount of data into smaller blocks that can be transmitted or encoded more efficiently.
///
/// # Arguments
///
///    * b: Maximum Source Block Length, i.e., the maximum number of source symbols per source block.
///
///    * l: Transfer Length in octets.
///
///    * e: Encoding Symbol Length in octets.
///
/// # Returns
///
/// The function returns a tuple of four values:
///     * a_large: The length of each of the larger source blocks in symbols.
///     * a_small: The length of each of the smaller source blocks in symbols.
///     * nb_a_large: The number of blocks composed of a_large symbols.
///     * nb_blocks: The total number of blocks.
///

func BlockPartitioning(b, l, e uint64) (uint64, uint64, uint64, uint64) {
	if b == 0 {
		log.Println("Maximum Source Block Length can not be 0")
		return 0, 0, 0, 0
	}

	if e == 0 {
		log.Println("Encoding Symbol Length can not be 0")
	}

	t := tools.DivCeil(l, e)
	n := tools.DivCeil(t, b)
	log.Printf("t=%d n=%d b=%d l=%d e=%d\n", t, n, b, l, e)

	if n == 0 {
		return 0, 0, 0, 0
	}

	aLarge := tools.DivCeil(t, n)
	aSmall := tools.DivCeil(t, n)
	nbALarge := t - (aSmall * n)
	nbBlocks := n
	return aLarge, aSmall, nbALarge, nbBlocks
}

/// Calculates the size of a block in octets.
///
/// # Arguments
///
/// * `a_large`: The length of each of the larger source blocks in symbols.
/// * `a_small`: The length of each of the smaller source blocks in symbols.
/// * `nb_a_large`: The number of blocks composed of `a_large` symbols.
/// * `l`: Transfer length in octets.
/// * `e`: Encoding symbol length in octets.
/// * `sbn`: Source block number.
///
/// # Returns
///
/// The size of the block in octets.
///

func BlockLength(aLarge, aSmall, nbALarge, l, e uint64, sbn uint32) uint64 {
	sbn64 := uint64(sbn)

	largeBlockSize := aLarge * e
	smallBlockSize := aSmall * e

	if sbn64+1 < nbALarge {
		return largeBlockSize
	}

	if sbn64+1 == nbALarge {
		largeSize := nbALarge * largeBlockSize
		if largeSize <= 1 {
			return largeBlockSize
		}

		return l - ((nbALarge - sbn64 - 1) * largeBlockSize)
	}

	l = l - (nbALarge * largeBlockSize)
	sbn64 = sbn64 - nbALarge
	smallSize := (sbn64 + 1) * smallBlockSize
	if smallSize <= l {
		return smallBlockSize
	}

	return l - (sbn64 * smallBlockSize)
}
